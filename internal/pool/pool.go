// Package pool 是内存账号注册表：持有启用账号的快照，负责选号、账号 × 模型冷却和每账号并发上限。
//
// 快照在启动时和账号变更后从库整体重新加载（Reload），选号和列模型不读库。
// 冷却在内存里生效，同时写库，启动时读回（LoadCooldowns），重启不丢；
// 并发计数、瞬时限流的连续次数、轮询游标只在进程内存。多实例部署再引入共享存储。
package pool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/store"
)

// ErrNoAccount 表示当前没有可用账号。没有任何可调度账号时直接返回它；支持该模型的账号暂时都不可用
// （冷却、并发已满、本次请求已试过）时返回 *UnavailableError，errors.Is(err, ErrNoAccount) 也为真。
var ErrNoAccount = errors.New("pool: no available account")

// ErrModelNotFound 表示有可调度的账号，但没有一个账号的目录能解析出该模型。
var ErrModelNotFound = errors.New("pool: model not found")

// UnavailableError 表示支持该模型的账号暂时都不可用。
type UnavailableError struct {
	// RetryAt 是最早有账号冷却结束的时间；没有账号在冷却时为零值。
	RetryAt time.Time
	// Busy 表示至少有一个账号是因为并发已满被跳过的，等一等可能就有空位。
	Busy bool
	// preferBusy 表示要等的是优先账号的名额（严格粘性），别的账号可能有空位：等不到时改为临时借别的账号。
	preferBusy bool
}

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("pool: no available account (retry at %s, busy %v)", e.RetryAt.Format(time.RFC3339), e.Busy)
}

// Is 让 errors.Is(err, ErrNoAccount) 成立。
func (e *UnavailableError) Is(target error) bool { return target == ErrNoAccount }

// DefaultConcurrency 是每个账号默认的并发上限，可由 SetDefaultConcurrency 修改。
const DefaultConcurrency = 15

// strikeReset：距离上一次瞬时限流超过这么久，连续次数从头算。
const strikeReset = 10 * time.Minute

// longCooldown：剩余超过这么久的额度耗尽冷却，是上游明确给出周额度重置时间的。单模型额度（fetchAvailableModels）
// 在周额度没恢复时也可能显示有剩余（短窗口已重置），不能据此提前解除，否则会反复撞 429。
const longCooldown = 24 * time.Hour

// Account 是注册表里的账号快照：库里的账号加上解析好的模型目录。
// 快照只读，账号变更时整体替换，持有旧快照的请求不受影响。
type Account struct {
	*store.Account
	models map[string]antigravity.ModelInfo
	// known 是有模型目录（哪怕目录里只有占位条目）；没有目录的账号视为未知，什么模型都放行。
	known bool
	// weekly 是各模型所在组的每周额度（开了每周额度的账号拉到的分组额度，见 antigravity.WeeklyLimits）。
	// 账号的每周额度开关是否生效由号池判断（见 Pool.WeeklyOn），关掉后库里留着的旧数据不起作用。
	weekly map[string]antigravity.WeeklyLimit
}

// NewAccount 用库里的账号构造号池账号（解析模型目录）。号池只放启用的账号；检测停用的账号时直接用它构造。
// 上游的占位条目（见 antigravity.IsUnavailablePlaceholder）不算这个账号的模型。
func NewAccount(a *store.Account) *Account {
	catalog := antigravity.UnmarshalModels(a.ModelsJSON)
	models := make(map[string]antigravity.ModelInfo, len(catalog))
	usable := make([]antigravity.ModelInfo, 0, len(catalog))
	for _, m := range catalog {
		if !antigravity.IsUnavailablePlaceholder(m.ID) {
			models[m.ID] = m
			usable = append(usable, m)
		}
	}
	weekly := antigravity.WeeklyLimits(antigravity.UnmarshalQuotaSummary(a.QuotaSummaryJSON), usable)
	return &Account{Account: a, models: models, known: len(catalog) > 0, weekly: weekly}
}

// weeklyLimit 返回某模型所在组的每周额度；没有数据，或已过重置时间（还没拉到新数据，按已恢复算）时 ok 为 false。
func (a *Account) weeklyLimit(model string, now time.Time) (antigravity.WeeklyLimit, bool) {
	l, ok := a.weekly[model]
	if !ok || (!l.Reset.IsZero() && !now.Before(l.Reset)) {
		return antigravity.WeeklyLimit{}, false
	}
	return l, true
}

// Model 返回账号目录里某模型的信息。
func (a *Account) Model(id string) (antigravity.ModelInfo, bool) {
	m, ok := a.models[id]
	return m, ok
}

// resolve 在账号目录里解析请求的模型（见 antigravity.ResolveModel）。
// 目录为空时视为未知，原样放行，由上游兜底。
func (a *Account) resolve(requested, tier string) (string, bool) {
	if !a.known {
		return requested, true
	}
	return antigravity.ResolveModel(a.hasModel, requested, tier)
}

// MissingEnum 判断目录里有该模型但缺枚举名：一般是升级前存下的旧目录，需要刷新。
// 目录为空（未知）时返回 false，那种账号刷新了也拿不到。
func (a *Account) MissingEnum(model string) bool {
	if !a.known {
		return false
	}
	m, ok := a.models[model]
	return ok && m.Enum == ""
}

// remaining 返回账号目录里某模型的剩余额度比例；目录里没有该模型或没有额度信息时 ok 为 false。
func (a *Account) remaining(model string) (float64, bool) {
	m, ok := a.models[model]
	if !ok || m.RemainingFraction == nil {
		return 0, false
	}
	return *m.RemainingFraction, true
}

// SharedQuota 返回和 model 共用同一个额度窗口的其它模型（剩余比例和重置时间都相同），按 ID 排序。
// 上游按窗口算额度，一个模型额度耗尽，同窗口的模型也用不了。没有额度信息时返回 nil。
// 剩余 100% 时也返回 nil：没用过的窗口都是 100%，重置时间也可能相同（实测新号所有窗口一样），推不出是否同窗口；
// 这时额度耗尽说明目录数据已经过时，只冷却这一个模型。
func (a *Account) SharedQuota(model string) []string {
	m, ok := a.models[model]
	if !ok || m.RemainingFraction == nil || m.ResetTime == "" || *m.RemainingFraction >= 1 {
		return nil
	}
	var out []string
	for id, o := range a.models {
		if id != model && o.RemainingFraction != nil && *o.RemainingFraction == *m.RemainingFraction && o.ResetTime == m.ResetTime {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (a *Account) hasModel(id string) bool {
	_, ok := a.models[id]
	return ok
}

type cooldownKey struct {
	accountID int64
	model     string
}

type cooldown struct {
	until  time.Time
	reason string
	// set 是写入（或延长）的时间，从库读回的按读回时间算。只有在它之后刷新的额度才能用来提前解除。
	set time.Time
}

type strike struct {
	count int
	last  time.Time
}

// CooldownInfo 是一条生效中的冷却，供管理端展示。
type CooldownInfo struct {
	Model  string    `json:"model"`
	Until  time.Time `json:"-"`
	Reason string    `json:"reason"`
}

// Lease 是一次选号的结果：占用了账号的一个并发名额，用完必须调用 Release（可重复调用）。
type Lease struct {
	Account *Account
	Model   string // 在该账号目录里解析出的模型 ID
	// InUse、Limit 是选中时该账号的并发占用（含本次）和上限。
	InUse int
	Limit int
	// Affinity 是会话粘性的结果：没有优先账号时为空；选中优先账号为 AffinityHit；否则是没选它的原因
	// （AffinityUnavailable 等）。
	Affinity string

	pool *Pool
	once sync.Once
}

// Release 归还并发名额。
func (l *Lease) Release() {
	if l.pool == nil {
		return // DetachedLease 不占名额
	}
	l.once.Do(func() { l.pool.release(l.Account.ID) })
}

// DetachedLease 返回不经过选号、不占并发名额的租约（账号检测用，停用的账号也能用），Release 什么也不做。
// InUse、Limit 为 0。
func DetachedLease(a *Account, model string) *Lease {
	return &Lease{Account: a, Model: model}
}

// Pool 持有启用账号的快照，按模型可用性、冷却和并发选号。
type Pool struct {
	store *store.Store
	enc   store.Encryptor

	// reloadMu 串行化 Reload，保证后发起的重新加载不会被先发起的旧快照覆盖。
	reloadMu sync.Mutex
	accounts atomic.Pointer[[]*Account] // 按 ID 升序

	cursor       atomic.Uint64
	defaultLimit atomic.Int64
	// lowQuota 是避让阈值（math.Float64bits 存的比例）：剩余额度低于它的账号先不选，0 表示不避让。
	lowQuota atomic.Uint64
	// requireProxy 为 true 时没绑定代理的账号不参与调度。
	requireProxy atomic.Bool
	// weeklyQuota 是每周额度的全局开关，账号可以单独开、关（store.Account.WeeklyQuota）。
	weeklyQuota atomic.Bool

	mu        sync.Mutex
	cooldowns map[cooldownKey]cooldown
	inflight  map[int64]int
	freed     chan struct{} // 有名额释放时关闭并换新，唤醒等待者
	strikes   map[int64]strike
	// paused 是整个账号暂停调度到什么时候（代理连不上时），只在内存里，不落库。
	paused map[int64]time.Time
	// health 是各账号近期的失败率和首字延迟，智能选号用，只在内存里。
	health map[int64]*health
	// strategy 是选号方式（Strategy*），空串按轮询。
	strategy atomic.Value
}

// New 构造一个空号池，使用前需调用 Reload（和 LoadCooldowns）。
func New(st *store.Store, enc store.Encryptor) *Pool {
	p := &Pool{
		store:     st,
		enc:       enc,
		cooldowns: map[cooldownKey]cooldown{},
		inflight:  map[int64]int{},
		freed:     make(chan struct{}),
		strikes:   map[int64]strike{},
		paused:    map[int64]time.Time{},
		health:    map[int64]*health{},
	}
	p.defaultLimit.Store(DefaultConcurrency)
	return p
}

// Reload 从库重新加载启用账号，整体替换快照。启动时和账号变更（导入、同步、启停、删除、
// refresh token 轮换、修改并发上限）落库后调用。
func (p *Pool) Reload(ctx context.Context) error {
	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()
	rows, err := p.store.ListEnabledAccounts(ctx, p.enc)
	if err != nil {
		return err
	}
	accounts := make([]*Account, 0, len(rows))
	for _, a := range rows {
		accounts = append(accounts, NewAccount(a))
	}
	p.accounts.Store(&accounts)
	return nil
}

// LoadCooldowns 从库读回未过期的冷却（顺手清理过期的），合并进内存。启动时调用一次；
// 运行中内存是准的，库只是持久化副本。
func (p *Pool) LoadCooldowns(ctx context.Context) error {
	rows, err := p.store.ActiveCooldowns(ctx, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range rows {
		p.setCooldownLocked(cooldownKey{c.AccountID, c.Model}, time.UnixMilli(c.Until), c.Reason)
	}
	return nil
}

// candidate 是选号时一个能用的账号和它解析出的模型。
type candidate struct {
	account *Account
	model   string
}

// SetDefaultConcurrency 设置账号没有单独设置时的并发上限，立即生效。
func (p *Pool) SetDefaultConcurrency(n int) {
	if n > 0 {
		p.defaultLimit.Store(int64(n))
	}
}

// SetLowQuota 设置避让阈值（0～1 的比例）：某账号对请求的模型剩余额度低于它时先不选；能选的都低于它时不避让。
func (p *Pool) SetLowQuota(threshold float64) {
	p.lowQuota.Store(math.Float64bits(threshold))
}

// SetWeeklyQuota 设置每周额度的全局开关。
func (p *Pool) SetWeeklyQuota(on bool) {
	p.weeklyQuota.Store(on)
}

// WeeklyOn 报告账号的每周额度是否生效：账号自己设了开、关就按它，否则跟随全局开关。只有生效的账号才拉每周额度，
// 选号也只看生效账号的每周额度。
func (p *Pool) WeeklyOn(a *store.Account) bool {
	switch a.WeeklyQuota {
	case store.WeeklyQuotaOn:
		return true
	case store.WeeklyQuotaOff:
		return false
	}
	return p.weeklyQuota.Load()
}

// weekly 返回账号某模型生效中的每周额度（见 Account.weeklyLimit），账号没开每周额度时 ok 为 false。
func (p *Pool) weekly(a *Account, model string, now time.Time) (antigravity.WeeklyLimit, bool) {
	if !p.WeeklyOn(a.Account) {
		return antigravity.WeeklyLimit{}, false
	}
	return a.weeklyLimit(model, now)
}

// quotaLeft 返回账号某模型的剩余额度：5 小时额度（模型目录）和每周额度（开了每周额度时）取较低的；都没有时 ok 为 false。
func (p *Pool) quotaLeft(a *Account, model string, now time.Time) (float64, bool) {
	rem, ok := a.remaining(model)
	if l, wok := p.weekly(a, model, now); wok && (!ok || l.Remaining < rem) {
		rem, ok = l.Remaining, true
	}
	return rem, ok
}

// Limit 返回账号的并发上限：单独设置的优先，否则用默认值。
func (p *Pool) Limit(a *store.Account) int {
	if a.MaxConcurrency > 0 {
		return a.MaxConcurrency
	}
	return int(p.defaultLimit.Load())
}

// Accounts 返回当前快照里的启用账号，按 ID 升序，只读。
func (p *Pool) Accounts() []*Account {
	return p.snapshot()
}

func (p *Pool) snapshot() []*Account {
	if s := p.accounts.Load(); s != nil {
		return *s
	}
	return nil
}

// 会话粘性的结果（Lease.Affinity）：选中了优先账号，或没选它的原因。
const (
	AffinityHit         = "hit"         // 选中了优先账号
	AffinityUnavailable = "unavailable" // 不在号池里（停用、删除、没同步出项目、代理停用）或目录里没有这个模型
	AffinityFailed      = "failed"      // 这次请求已经在它上面失败过
	AffinityPaused      = "paused"      // 代理连不上，暂停调度中
	AffinityCooldown    = "cooldown"    // 这个模型在冷却
	AffinityBusy        = "busy"        // 并发已满（不等它，直接换号）
	AffinityLowQuota    = "low_quota"   // 这个模型剩余额度低于避让阈值，而别的账号充足
)

// Select 为请求的模型选一个可用账号，占用它的一个并发名额；不等待。
// tier 用于家族名选档；exclude 是本次请求已经试过的账号，不再选；prefer 是优先的账号（会话粘性，0 表示没有）。
// 冷却按解析后的模型判断。
func (p *Pool) Select(requested, tier string, exclude map[int64]bool, prefer int64) (*Lease, error) {
	lease, _, err := p.pick(requested, tier, exclude, prefer, false)
	return lease, err
}

// Acquire 同 Select，但支持该模型的账号有因为并发已满而不可用的，就等名额释放，最多 wait。
// waitPrefer 为 true 时（严格粘性）优先账号只是并发已满，就先等它的名额，不借别的账号；等到 wait 用完还没空位，
// 再按 Select 借别的账号。等不到时返回最后一次的 *UnavailableError；ctx 取消时返回 ctx.Err()。
func (p *Pool) Acquire(ctx context.Context, requested, tier string, exclude map[int64]bool, prefer int64, waitPrefer bool, wait time.Duration) (*Lease, error) {
	deadline := time.Now().Add(wait)
	for {
		lease, freed, err := p.pick(requested, tier, exclude, prefer, waitPrefer)
		var ue *UnavailableError
		if err == nil || !errors.As(err, &ue) || !ue.Busy {
			return lease, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if ue.preferBusy {
				waitPrefer = false // 等不到优先账号的名额：临时借别的账号
				continue
			}
			return nil, err
		}
		// 有名额释放或等到时限都再选一次：到时限后的这一次不再等（见上），严格粘性在这时改为借别的账号。
		timer := time.NewTimer(remaining)
		select {
		case <-freed:
			timer.Stop()
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
}

// pick 选号并占用名额。prefer 不为 0 时它能接就选它（会话粘性），不能接时按轮询选别的，Lease.Affinity 记下原因；
// waitPrefer 为 true 且它只是并发已满（额度也不低）时不选别的，返回 preferBusy 的 *UnavailableError 让调用方等它。
// 返回的 freed 是选号时刻的释放通道，并发已满时可等它，不会漏掉中间的释放。
func (p *Pool) pick(requested, tier string, exclude map[int64]bool, prefer int64, waitPrefer bool) (*Lease, <-chan struct{}, error) {
	// 解析模型不需要锁。
	accounts := p.snapshot()
	supported := make([]candidate, 0, len(accounts))
	schedulable := 0
	requireProxy := p.requireProxy.Load()
	for _, a := range accounts {
		if a.ProjectID == "" {
			continue // 未同步出项目，不可调度
		}
		if requireProxy && a.ProxyURL == "" {
			continue // 设置了只用绑定代理的账号
		}
		if a.ProxyDisabled {
			continue // 绑定的代理停用了，不改成直连
		}
		schedulable++
		if model, ok := a.resolve(requested, tier); ok {
			supported = append(supported, candidate{a, model})
		}
	}
	if len(supported) == 0 {
		if schedulable > 0 {
			return nil, nil, ErrModelNotFound
		}
		return nil, nil, ErrNoAccount
	}

	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	var retryAt time.Time
	busy := false
	var preferBusy *candidate // 优先账号只是并发已满时是它
	// affinity 是优先账号的结果，先假定它不在号池里或不支持这个模型，下面逐项判断。
	affinity := ""
	if prefer != 0 {
		affinity = AffinityUnavailable
	}
	note := func(id int64, reason string) {
		if id == prefer {
			affinity = reason
		}
	}
	candidates := supported[:0:0]
	for _, c := range supported {
		if exclude[c.account.ID] {
			note(c.account.ID, AffinityFailed)
			continue
		}
		if until, ok := p.paused[c.account.ID]; ok {
			if now.Before(until) {
				if retryAt.IsZero() || until.Before(retryAt) {
					retryAt = until
				}
				note(c.account.ID, AffinityPaused)
				continue
			}
			delete(p.paused, c.account.ID)
		}
		if cd, ok := p.activeCooldownLocked(cooldownKey{c.account.ID, c.model}, now); ok {
			if retryAt.IsZero() || cd.until.Before(retryAt) {
				retryAt = cd.until
			}
			note(c.account.ID, AffinityCooldown)
			continue
		}
		// 每周额度用完：重置前不选，和冷却一样对待（上游这时只会回 429）。
		if l, ok := p.weekly(c.account, c.model, now); ok && l.Remaining <= 0 {
			if !l.Reset.IsZero() && (retryAt.IsZero() || l.Reset.Before(retryAt)) {
				retryAt = l.Reset
			}
			note(c.account.ID, AffinityCooldown)
			continue
		}
		if p.inflight[c.account.ID] >= p.Limit(c.account.Account) {
			busy = true
			note(c.account.ID, AffinityBusy)
			if c.account.ID == prefer {
				preferBusy = &c
			}
			continue
		}
		candidates = append(candidates, c)
	}
	threshold := math.Float64frombits(p.lowQuota.Load())
	// 严格粘性：优先账号只是并发已满就等它，额度低于避让阈值的不等（空出来也会先避开它）。
	if waitPrefer && preferBusy != nil {
		if rem, ok := p.quotaLeft(preferBusy.account, preferBusy.model, now); threshold <= 0 || !ok || rem >= threshold {
			return nil, p.freed, &UnavailableError{RetryAt: retryAt, Busy: true, preferBusy: true}
		}
	}
	if len(candidates) == 0 {
		return nil, p.freed, &UnavailableError{RetryAt: retryAt, Busy: busy}
	}
	// 额度避让：先排除这个模型剩余额度低于阈值的账号；都低时不避让。没有额度信息的视为正常。
	// 开了每周额度的账号按 5 小时和每周额度的较低值算。
	if threshold > 0 {
		healthy := candidates[:0:0]
		for _, c := range candidates {
			if rem, ok := p.quotaLeft(c.account, c.model, now); !ok || rem >= threshold {
				healthy = append(healthy, c)
			} else {
				note(c.account.ID, AffinityLowQuota)
			}
		}
		if len(healthy) > 0 {
			candidates = healthy
		}
	}
	// 会话粘性：优先账号能接就用它；否则按选号方式选（见 strategy.go）。
	var c candidate
	found := false
	for _, x := range candidates {
		if x.account.ID == prefer {
			c, found, affinity = x, true, AffinityHit
			break
		}
	}
	if !found {
		c = p.chooseLocked(candidates, now)
	}
	p.inflight[c.account.ID]++
	return &Lease{Account: c.account, Model: c.model, InUse: p.inflight[c.account.ID], Limit: p.Limit(c.account.Account),
		Affinity: affinity, pool: p}, nil, nil
}

// HoldWindow 是会话粘性临时借号时保留原绑定的上限：原账号在这段时间内能恢复，就只是临时借号、不改绑。
const HoldWindow = 5 * time.Minute

// Recoverable 报告账号能否在 within 内重新接这个模型的请求：还在号池里、能调度、目录里有这个模型，冷却和代理暂停
// 在 within 内结束，每周额度没用完，额度不低于避让阈值。并发已满不影响（名额很快会释放）。会话粘性换号后据此
// 决定临时借号（保留原绑定）还是改绑。
func (p *Pool) Recoverable(accountID int64, requested, tier string, within time.Duration) bool {
	var a *Account
	for _, x := range p.snapshot() {
		if x.ID == accountID {
			a = x
			break
		}
	}
	if a == nil || a.ProjectID == "" || a.ProxyDisabled || (p.requireProxy.Load() && a.ProxyURL == "") {
		return false
	}
	model, ok := a.resolve(requested, tier)
	if !ok {
		return false
	}
	now := time.Now()
	limit := now.Add(within)
	p.mu.Lock()
	defer p.mu.Unlock()
	if until, ok := p.paused[accountID]; ok && until.After(limit) {
		return false
	}
	if cd, ok := p.activeCooldownLocked(cooldownKey{accountID, model}, now); ok && cd.until.After(limit) {
		return false
	}
	if l, ok := p.weekly(a, model, now); ok && l.Remaining <= 0 {
		return false
	}
	if threshold := math.Float64frombits(p.lowQuota.Load()); threshold > 0 {
		if rem, ok := p.quotaLeft(a, model, now); ok && rem < threshold {
			return false
		}
	}
	return true
}

func (p *Pool) release(accountID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inflight[accountID]--; p.inflight[accountID] <= 0 {
		delete(p.inflight, accountID)
	}
	close(p.freed)
	p.freed = make(chan struct{})
}

// InFlight 返回账号当前占用的并发名额数。
func (p *Pool) InFlight(accountID int64) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inflight[accountID]
}

// InFlightAll 返回所有账号当前占用的并发名额数（账号 ID → 占用数，占用为 0 的不在表里），一次加锁取快照。
func (p *Pool) InFlightAll() map[int64]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.inflight)
}

// Lookup 在启用账号的目录里解析请求的模型，返回第一个解析成功的模型信息（ID 是解析后的）。
func (p *Pool) Lookup(requested, tier string) (antigravity.ModelInfo, bool) {
	for _, a := range p.snapshot() {
		if !a.known {
			continue
		}
		if id, ok := antigravity.ResolveModel(a.hasModel, requested, tier); ok {
			return a.models[id], true
		}
	}
	return antigravity.ModelInfo{}, false
}

// Models 返回启用账号模型目录的并集，按 ID 排序。同一个模型取第一个账号的条目，对外列出的标记取并集：
// 任一账号的目录列出它就列出（各账号的目录可能新旧不一，见 antigravity.PublicModels）。
func (p *Pool) Models() []antigravity.ModelInfo {
	seen := map[string]antigravity.ModelInfo{}
	for _, a := range p.snapshot() {
		for id, m := range a.models {
			old, ok := seen[id]
			if !ok {
				seen[id] = m
				continue
			}
			old.Listed = old.Listed || m.Listed
			if old.Family == "" {
				old.Family, old.FamilyDisplayName = m.Family, m.FamilyDisplayName
			}
			seen[id] = old
		}
	}
	out := make([]antigravity.ModelInfo, 0, len(seen))
	for _, m := range seen {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// activeCooldownLocked 返回生效中的冷却，过期的顺手删掉。调用方持有 p.mu。
func (p *Pool) activeCooldownLocked(key cooldownKey, now time.Time) (cooldown, bool) {
	cd, ok := p.cooldowns[key]
	if !ok {
		return cooldown{}, false
	}
	if now.Before(cd.until) {
		return cd, true
	}
	delete(p.cooldowns, key)
	return cooldown{}, false
}

// setCooldownLocked 写入内存冷却，只延长不缩短。返回是否有变化。调用方持有 p.mu。
func (p *Pool) setCooldownLocked(key cooldownKey, until time.Time, reason string) bool {
	if existing, ok := p.cooldowns[key]; ok && !until.After(existing.until) {
		return false
	}
	p.cooldowns[key] = cooldown{until: until, reason: reason, set: time.Now()}
	return true
}

// Cooldown 让账号的若干模型冷却到 until，reason 是分类（如 rate_limit）。不会缩短已有的更晚截止时间。
// 内存立即生效，有变化的在一个事务里写库（不随请求取消）：额度耗尽时同窗口的模型一起冷却，换号前只等一次写库。
// 写库失败只记日志，重启后这些冷却会丢。
func (p *Pool) Cooldown(ctx context.Context, accountID int64, until time.Time, reason string, models ...string) {
	if !until.After(time.Now()) {
		return
	}
	var changed []store.Cooldown
	p.mu.Lock()
	for _, m := range models {
		if p.setCooldownLocked(cooldownKey{accountID, m}, until, reason) {
			changed = append(changed, store.Cooldown{AccountID: accountID, Model: m, Until: until.UnixMilli(), Reason: reason})
		}
	}
	p.mu.Unlock()
	if len(changed) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := p.store.SetCooldowns(ctx, changed...); err != nil {
		slog.ErrorContext(ctx, "冷却写库失败", "account_id", accountID, "models", models, "err", err)
	}
}

// ReconcileCooldowns 按当前快照里的额度，提前解除已经恢复的"额度耗尽"冷却：冷却写入后刷新过额度，
// 该账号这个模型剩余额度大于 0，且冷却剩余不超过 longCooldown；开了每周额度的账号还要每周额度没用完
// （5 小时额度恢复了、每周额度还是 0 时上游照样回 429）。内存和库都删。返回解除的条数。刷新额度后调用。
func (p *Pool) ReconcileCooldowns(ctx context.Context) int {
	now := time.Now()
	accounts := map[int64]*Account{}
	for _, a := range p.snapshot() {
		accounts[a.ID] = a
	}
	var cleared []store.Cooldown
	p.mu.Lock()
	for key, cd := range p.cooldowns {
		if cd.reason != string(antigravity.KindQuotaExhausted) || cd.until.Sub(now) > longCooldown {
			continue
		}
		a := accounts[key.accountID]
		if a == nil {
			continue
		}
		if !time.UnixMilli(a.ModelsRefreshedAt).After(cd.set) {
			continue // 额度是冷却之前取的，不能说明已恢复
		}
		if l, ok := p.weekly(a, key.model, now); ok && l.Remaining <= 0 {
			continue
		}
		if rem, ok := a.remaining(key.model); ok && rem > 0 {
			delete(p.cooldowns, key)
			cleared = append(cleared, store.Cooldown{AccountID: key.accountID, Model: key.model})
		}
	}
	p.mu.Unlock()
	if len(cleared) == 0 {
		return 0
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, c := range cleared {
		slog.InfoContext(ctx, "额度已恢复，提前解除冷却", "account_id", c.AccountID, "model", c.Model)
	}
	if err := p.store.DeleteCooldowns(ctx, cleared...); err != nil {
		slog.ErrorContext(ctx, "删除冷却失败", "count", len(cleared), "err", err)
	}
	return len(cleared)
}

// Cooldowns 返回生效中的冷却，按账号分组，每组按模型排序。
func (p *Pool) Cooldowns() map[int64][]CooldownInfo {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[int64][]CooldownInfo{}
	for key := range p.cooldowns {
		if cd, ok := p.activeCooldownLocked(key, now); ok {
			out[key.accountID] = append(out[key.accountID], CooldownInfo{Model: key.model, Until: cd.until, Reason: cd.reason})
		}
	}
	for _, list := range out {
		sort.Slice(list, func(i, j int) bool { return list[i].Model < list[j].Model })
	}
	return out
}

// NoteRateLimit 记一次没带恢复时间的瞬时限流，返回该账号连续第几次。
// 距上一次超过 strikeReset，或中间成功过（NoteSuccess），从 1 重新算。
func (p *Pool) NoteRateLimit(accountID int64) int {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.strikes[accountID]
	if now.Sub(s.last) > strikeReset {
		s.count = 0
	}
	s.count++
	s.last = now
	p.strikes[accountID] = s
	return s.count
}

// SetRequireProxy 设置是否只用绑定了代理的账号。
func (p *Pool) SetRequireProxy(on bool) {
	p.requireProxy.Store(on)
}

// Pause 让整个账号暂停调度到 until（代理连不上时），只在内存里；不缩短已有的更晚截止时间。
func (p *Pool) Pause(accountID int64, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cur, ok := p.paused[accountID]; !ok || until.After(cur) {
		p.paused[accountID] = until
	}
}

// Paused 返回暂停调度中的账号和截止时间。
func (p *Pool) Paused() map[int64]time.Time {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[int64]time.Time{}
	for id, until := range p.paused {
		if now.Before(until) {
			out[id] = until
		}
	}
	return out
}

// NoteSuccess 记一次成功：清零账号的瞬时限流连续次数，计入近期失败率（见 strategy.go）。firstByte 是流式请求
// 从发出到收到第一帧的时间，计入首字延迟；非流式和账号检测传 0（整段生成完才返回，不可比）。
func (p *Pool) NoteSuccess(accountID int64, firstByte time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.strikes, accountID)
	p.healthLocked(accountID).note(false, firstByte, time.Now())
}
