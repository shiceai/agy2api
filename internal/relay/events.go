package relay

import "time"

// 值得告警的事件类型（见 Event）。
const (
	EventAccountBanned      = "account_banned"      // 账号因条款封禁被停用
	EventTokenInvalid       = "token_invalid"       // 账号因 refresh token 失效被停用
	EventValidationRequired = "validation_required" // 账号需要验证（该模型冷却中）
	EventNoAccount          = "no_account"          // 请求的模型没有可用账号（都在冷却、暂停或已停用，不含并发满）
	EventPoolPaused         = "pool_paused"         // 封禁后全池暂停开始
	EventPoolResumed        = "pool_resumed"        // 全池暂停结束（到时或管理端手动恢复）
)

// Event 是执行层里管理员应该知道的事，由服务层转成告警通知（见 SetEventHandler）。
type Event struct {
	Type      string
	AccountID int64     // 涉及的账号（没有时为 0）
	Model     string    // 涉及的模型（客户端请求的名字）
	Link      string    // 上游给的处理链接：申诉地址或验证地址
	RetryAt   time.Time // EventNoAccount：最早有账号恢复的时间，不知道时为零值
	Until     time.Time // EventPoolPaused：暂停到什么时候
	Manual    bool      // EventPoolResumed：是否管理端手动恢复
	At        time.Time
}

// SetEventHandler 设置事件回调，只能在开始服务前调用一次。回调在请求路径上同步执行，要快（不读库、不发网络请求）。
func (r *Relay) SetEventHandler(fn func(Event)) {
	r.onEvent = fn
}

func (r *Relay) emit(e Event) {
	if r.onEvent == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	r.onEvent(e)
}
