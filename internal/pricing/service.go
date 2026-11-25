package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shiceai/agy2api/internal/store"
)

const (
	// syncInterval 是官方价格的同步间隔；有来源失败时 retryInterval 后重试。
	syncInterval  = 24 * time.Hour
	retryInterval = time.Hour
	// startupDelay 是启动后第一次同步前的等待（距上次同步已超过间隔时），避开启动时的其它工作。
	startupDelay  = 10 * time.Second
	syncTimeout   = 2 * time.Minute
	fetchTimeout  = 30 * time.Second
	bodyLimit     = 4 << 20
	backfillBatch = 500
	// statusKey 是最近一次同步结果在 settings 里的键。
	statusKey = "price_sync"
)

// ErrInvalidModel 表示手动设价的模型名不合法。
var ErrInvalidModel = errors.New("pricing: invalid model name")

// ErrInvalidPrice 表示手动设的价格不合法。
var ErrInvalidPrice = errors.New("pricing: invalid price")

var manualModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// source 是一个官方价格来源：一个价格页和它的解析。
type source struct {
	Vendor string
	URL    string
	parse  func([]byte) ([]Entry, []Skip, error)
}

// Status 是最近一次同步的结果，存在 settings 的 price_sync。
type Status struct {
	AttemptAt int64          `json:"attempt_at"`
	Sources   []SourceStatus `json:"sources"`
}

// SourceStatus 是一个来源最近一次同步的结果。
type SourceStatus struct {
	Vendor string `json:"vendor"`
	URL    string `json:"url"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	// Prices 是读出的价格数，Skipped 是页面上读不出价格的型号。
	Prices  int    `json:"prices"`
	Skipped []Skip `json:"skipped,omitempty"`
	// SuccessAt 是这个来源最近一次同步成功的时间，失败时保留之前的。
	SuccessAt int64 `json:"success_at"`
}

// Service 维护价格表：从官方价格页同步、手动设价，价格变化后补算还没算费用的用量记录。
// 价格表在内存里，算费用不读库。
type Service struct {
	store   *store.Store
	client  *http.Client
	sources []source
	book    atomic.Pointer[Book]
	// mu 串行化同步、改价和补算。
	mu sync.Mutex

	started   bool
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// New 构造价格服务，价格表为空；使用前调用 Load。
func New(st *store.Store) *Service {
	s := &Service{
		store:  st,
		client: &http.Client{Timeout: fetchTimeout},
		sources: []source{
			{Vendor: VendorGoogle, URL: googlePricingURL, parse: parseGoogle},
			{Vendor: VendorAnthropic, URL: anthropicPricingURL, parse: parseAnthropic},
		},
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	s.book.Store(NewBook(nil))
	return s
}

// Load 从库读入价格表。
func (s *Service) Load(ctx context.Context) error {
	rows, err := s.store.ListModelPrices(ctx)
	if err != nil {
		return err
	}
	entries := make([]Entry, 0, len(rows))
	for _, r := range rows {
		var p Price
		if err := json.Unmarshal([]byte(r.PriceJSON), &p); err != nil {
			slog.Error("价格数据损坏，跳过", "model", r.Model, "source", r.Source, "err", err)
			continue
		}
		entries = append(entries, Entry{Model: r.Model, Source: r.Source, Vendor: r.Vendor, Name: r.Name, Price: p, UpdatedAt: r.UpdatedAt})
	}
	s.book.Store(NewBook(entries))
	return nil
}

// Book 返回当前的价格表快照。
func (s *Service) Book() *Book {
	return s.book.Load()
}

// Cost 按 at 时刻有效的价格算一条用量的费用；没有 token 或没有这个模型的价格时返回 nil。
func (s *Service) Cost(model string, at time.Time, u Usage) *store.UsageCost {
	return costOf(s.Book(), model, at, u)
}

func costOf(b *Book, model string, at time.Time, u Usage) *store.UsageCost {
	if u.Prompt+u.Output+u.Thoughts <= 0 {
		return nil
	}
	e, ok := b.Find(model)
	if !ok {
		return nil
	}
	a := e.Price.At(u.Prompt, at)
	cache := a.Input
	if a.CacheRead != nil {
		cache = *a.CacheRead
	}
	return &store.UsageCost{Nanos: a.Nanos(u), Model: e.Model, Source: e.Source,
		Input: a.Input, CacheRead: cache, Output: a.Output, Tier: a.Tier, Promo: a.Promo}
}

// Status 返回最近一次同步的结果，还没同步过时为零值。
func (s *Service) Status(ctx context.Context) (Status, error) {
	var st Status
	raw, ok, err := s.store.GetSetting(ctx, statusKey)
	if err != nil || !ok {
		return st, err
	}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		slog.Error("价格同步状态损坏，按未同步处理", "err", err)
		return Status{}, nil
	}
	return st, nil
}

// Sync 从各官方价格页同步价格。来源之间互不影响：读取、解析失败，或读出的价格不到上次的八成（多半是页面改版）时，
// 这个来源保留原价格。返回本次结果；只有写库失败才返回错误。之后补算还没算费用的用量记录。
func (s *Service) Sync(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, err := s.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	have := map[string]int{}
	for _, e := range s.Book().official {
		have[e.Vendor]++
	}
	now := time.Now().UnixMilli()
	st := Status{AttemptAt: now}
	var rows []store.ModelPrice
	for _, src := range s.sources {
		ss := SourceStatus{Vendor: src.Vendor, URL: src.URL}
		for _, p := range prev.Sources {
			if p.Vendor == src.Vendor {
				ss.SuccessAt = p.SuccessAt
			}
		}
		entries, skipped, err := s.fetch(ctx, src)
		if err == nil && len(entries)*5 < have[src.Vendor]*4 {
			err = fmt.Errorf("只读出 %d 个价格，不到现有的八成（%d 个），可能是页面改版，保留原价格", len(entries), have[src.Vendor])
		}
		if err != nil {
			ss.Error = err.Error()
			slog.WarnContext(ctx, "同步官方价格失败", "vendor", src.Vendor, "err", err)
		} else {
			ss.OK, ss.Prices, ss.Skipped, ss.SuccessAt = true, len(entries), skipped, now
			for _, e := range entries {
				raw, _ := json.Marshal(e.Price)
				rows = append(rows, store.ModelPrice{Model: e.Model, Source: SourceOfficial, Vendor: e.Vendor,
					Name: e.Name, PriceJSON: string(raw), UpdatedAt: now})
			}
			slog.InfoContext(ctx, "已同步官方价格", "vendor", src.Vendor, "prices", len(entries), "skipped", len(skipped))
		}
		st.Sources = append(st.Sources, ss)
	}
	if len(rows) > 0 {
		if err := s.store.UpsertModelPrices(ctx, rows); err != nil {
			return st, err
		}
	}
	raw, _ := json.Marshal(st)
	if err := s.store.SetSetting(ctx, statusKey, string(raw)); err != nil {
		return st, err
	}
	if err := s.reloadAndBackfill(ctx); err != nil {
		return st, err
	}
	return st, nil
}

// fetch 读取并解析一个价格页。
func (s *Service) fetch(ctx context.Context, src source) ([]Entry, []Skip, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, nil, err
	}
	// Google 按这个请求头返回 Markdown；Anthropic 的地址本身就是 .md。
	req.Header.Set("Accept", "text/markdown")
	req.Header.Set("User-Agent", "agy2api")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("读取价格页失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("价格页返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
	if err != nil {
		return nil, nil, fmt.Errorf("读取价格页失败：%w", err)
	}
	if len(body) > bodyLimit {
		return nil, nil, errors.New("价格页超过大小上限")
	}
	return src.parse(body)
}

// SetManual 手动设置一个模型的价格（号池里的模型名或家族名），优先于官方价。之后补算还没算费用的用量记录。
func (s *Service) SetManual(ctx context.Context, model string, p Price) error {
	if !manualModelRe.MatchString(model) {
		return ErrInvalidModel
	}
	if !p.Valid() {
		return ErrInvalidPrice
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(p)
	if err := s.store.UpsertModelPrices(ctx, []store.ModelPrice{{Model: model, Source: SourceManual, PriceJSON: string(raw)}}); err != nil {
		return err
	}
	return s.reloadAndBackfill(ctx)
}

// DeleteManual 删除手动价，恢复官方价（没有官方价的模型之后不再计费）。不存在时返回 store.ErrNotFound。
func (s *Service) DeleteManual(ctx context.Context, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.DeleteModelPrice(ctx, model, SourceManual); err != nil {
		return err
	}
	return s.Load(ctx)
}

// reloadAndBackfill 重新读入价格表，再补算还没算费用的用量记录。调用方持有 mu。
func (s *Service) reloadAndBackfill(ctx context.Context) error {
	if err := s.Load(ctx); err != nil {
		return err
	}
	n, err := s.backfill(ctx)
	if n > 0 {
		slog.InfoContext(ctx, "已补算用量费用", "records", n)
	}
	return err
}

// backfill 给还没算费用、有 token 的用量记录按记录时有效的价格补算费用，返回补算的条数。
// 仍然没有价格的记录保持未算，之后有了价格再补。已算过的记录不随改价变化。
func (s *Service) backfill(ctx context.Context) (int, error) {
	book := s.Book()
	var after int64
	total := 0
	for {
		rows, err := s.store.ListUnpricedUsage(ctx, after, backfillBatch)
		if err != nil {
			return total, err
		}
		var updates []store.UsageCostUpdate
		for _, r := range rows {
			after = r.ID
			c := costOf(book, r.Model, time.UnixMilli(r.CreatedAt), Usage{Prompt: r.Prompt, Cached: r.Cached, Output: r.Output, Thoughts: r.Thoughts})
			if c != nil {
				c.Backfilled = true
				updates = append(updates, store.UsageCostUpdate{ID: r.ID, Cost: c})
			}
		}
		if len(updates) > 0 {
			if err := s.store.SetUsageCosts(ctx, updates); err != nil {
				return total, err
			}
			total += len(updates)
		}
		if len(rows) < backfillBatch {
			return total, nil
		}
	}
}

// Start 启动后台定时同步：距上次同步超过间隔时，启动后稍等就同步一次。只能调用一次，用完调用 Close。
func (s *Service) Start() {
	s.started = true
	go func() {
		defer close(s.done)
		t := time.NewTimer(s.nextDelay())
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
				if _, err := s.Sync(ctx); err != nil {
					slog.Error("同步官方价格失败", "err", err)
				}
				cancel()
				t.Reset(s.nextDelay())
			}
		}
	}()
}

// nextDelay 按上次同步的结果算下次同步前的等待：都成功时间隔 syncInterval，有来源失败时 retryInterval。
func (s *Service) nextDelay() time.Duration {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := s.Status(ctx)
	if err != nil || st.AttemptAt == 0 {
		return startupDelay
	}
	interval := syncInterval
	for _, src := range st.Sources {
		if !src.OK {
			interval = retryInterval
		}
	}
	return max(time.Until(time.UnixMilli(st.AttemptAt).Add(interval)), startupDelay)
}

// Close 停止后台同步，等进行中的同步结束；ctx 到期则不再等待。
func (s *Service) Close(ctx context.Context) error {
	if !s.started {
		return nil
	}
	s.closeOnce.Do(func() { close(s.stop) })
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
