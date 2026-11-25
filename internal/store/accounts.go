package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrNotFound 表示按条件未查到记录。
var ErrNotFound = errors.New("store: not found")

// Account 是一个 Antigravity OAuth 账号。敏感字段以加密形态存库，
// 这里的 RefreshToken / ClientSecret 字段在读出后由上层解密填充（见 DAO 方法）。
type Account struct {
	ID             int64
	Email          string
	Name           string
	ProjectID      string
	UserProjectID  string
	RefreshToken   string // 明文（已解密）
	ClientID       string
	ClientSecret   string // 明文（已解密）
	Scope          string
	UserAgent      string // 设备画像：该账号 Cloud Code 请求用的 User-Agent
	Status         string // enabled | disabled
	DisabledReason string
	// MaxConcurrency 是单独设置的并发上限，0 表示跟随全局默认值。
	MaxConcurrency int
	ModelsJSON     string // 模型目录快照（原始 JSON 文本），含每个模型的额度
	SyncedAt       int64  // 上次同步的 Unix 毫秒
	// ModelsRefreshedAt 是模型目录（含额度）上次刷新的 Unix 毫秒，同步和后台刷新都会更新。
	ModelsRefreshedAt int64
	// SubscriptionJSON 是 loadCodeAssist 返回的订阅信息（JSON），SubscriptionCheckedAt 是上次检查的 Unix 毫秒。
	SubscriptionJSON      string
	SubscriptionCheckedAt int64
	// ProxyID 是绑定的代理，0 表示直连；ProxyURL 是它的地址（明文，已解密），ProxyDisabled 是它是否停用，
	// 读账号时一起带出。
	ProxyID       int64
	ProxyURL      string
	ProxyDisabled bool
	CreatedAt     int64
	UpdatedAt     int64
}

// Enabled 报告账号是否处于可调度状态。
func (a *Account) Enabled() bool { return a.Status == StatusEnabled }

func nowMs() int64 { return time.Now().UnixMilli() }

// CreateAccount 插入一个账号；email 冲突时返回已有记录的 ID 与 ErrConflict。
func (s *Store) CreateAccount(ctx context.Context, enc Encryptor, a *Account) error {
	rtEnc, err := enc.Encrypt(a.RefreshToken)
	if err != nil {
		return err
	}
	csEnc, err := enc.Encrypt(a.ClientSecret)
	if err != nil {
		return err
	}
	now := nowMs()
	a.CreatedAt, a.UpdatedAt = now, now
	if a.Status == "" {
		a.Status = StatusEnabled
	}
	if a.ModelsJSON == "" {
		a.ModelsJSON = "[]"
	}
	res, err := s.execCtx(ctx, `
		INSERT INTO accounts
		  (email,name,project_id,user_project_id,refresh_token_enc,client_id,client_secret_enc,
		   scope,user_agent,status,disabled_reason,models_json,synced_at,created_at,updated_at,
		   models_refreshed_at,subscription_json,subscription_checked_at,proxy_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(email) DO NOTHING`,
		a.Email, a.Name, a.ProjectID, a.UserProjectID, rtEnc, a.ClientID, csEnc,
		a.Scope, a.UserAgent, a.Status, a.DisabledReason, a.ModelsJSON, a.SyncedAt, a.CreatedAt, a.UpdatedAt,
		a.SyncedAt, a.SubscriptionJSON, a.SyncedAt, nullID(a.ProxyID))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	a.ID = id
	return nil
}

// ErrConflict 表示唯一约束冲突（如 email 已存在）。
var ErrConflict = errors.New("store: conflict")

func scanAccount(enc Encryptor, row interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	var rtEnc, csEnc, proxyEnc string
	if err := row.Scan(&a.ID, &a.Email, &a.Name, &a.ProjectID, &a.UserProjectID,
		&rtEnc, &a.ClientID, &csEnc, &a.Scope, &a.UserAgent, &a.Status, &a.DisabledReason,
		&a.MaxConcurrency, &a.ModelsJSON, &a.SyncedAt, &a.CreatedAt, &a.UpdatedAt,
		&a.ModelsRefreshedAt, &a.SubscriptionJSON, &a.SubscriptionCheckedAt, &a.ProxyID, &proxyEnc, &a.ProxyDisabled); err != nil {
		return nil, err
	}
	var err error
	if proxyEnc != "" {
		if a.ProxyURL, err = enc.Decrypt(proxyEnc); err != nil {
			return nil, err
		}
	}
	if a.RefreshToken, err = enc.Decrypt(rtEnc); err != nil {
		return nil, err
	}
	if a.ClientSecret, err = enc.Decrypt(csEnc); err != nil {
		return nil, err
	}
	return &a, nil
}

const accountCols = `id,email,name,project_id,user_project_id,refresh_token_enc,client_id,
	client_secret_enc,scope,user_agent,status,disabled_reason,max_concurrency,models_json,synced_at,created_at,updated_at,
	models_refreshed_at,subscription_json,subscription_checked_at,COALESCE(proxy_id,0),
	COALESCE((SELECT p.url_enc FROM proxies p WHERE p.id = accounts.proxy_id),''),
	COALESCE((SELECT p.status = 'disabled' FROM proxies p WHERE p.id = accounts.proxy_id),0)`

// GetAccount 按 ID 读取账号。
func (s *Store) GetAccount(ctx context.Context, enc Encryptor, id int64) (*Account, error) {
	row := s.reader.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE id=?`, id)
	a, err := scanAccount(enc, row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// GetAccountByEmail 按邮箱读取账号（邮箱唯一），没有时返回 ErrNotFound。
func (s *Store) GetAccountByEmail(ctx context.Context, enc Encryptor, email string) (*Account, error) {
	row := s.reader.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE email=?`, email)
	a, err := scanAccount(enc, row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// ListAccounts 返回全部账号，按创建时间升序。
func (s *Store) ListAccounts(ctx context.Context, enc Encryptor) ([]*Account, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT `+accountCols+` FROM accounts ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(enc, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListEnabledAccounts 返回可调度账号，用于选号。
func (s *Store) ListEnabledAccounts(ctx context.Context, enc Encryptor) ([]*Account, error) {
	rows, err := s.reader.QueryContext(ctx,
		`SELECT `+accountCols+` FROM accounts WHERE status='enabled' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(enc, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAccountSync 在一次同步后回写身份、项目、模型目录与凭据（refresh token 可能轮换）。
func (s *Store) UpdateAccountSync(ctx context.Context, enc Encryptor, a *Account) error {
	rtEnc, err := enc.Encrypt(a.RefreshToken)
	if err != nil {
		return err
	}
	csEnc, err := enc.Encrypt(a.ClientSecret)
	if err != nil {
		return err
	}
	a.UpdatedAt = nowMs()
	_, err = s.execCtx(ctx, `
		UPDATE accounts SET email=?,name=?,project_id=?,user_project_id=?,refresh_token_enc=?,
		  client_id=?,client_secret_enc=?,scope=?,user_agent=?,models_json=?,synced_at=?,updated_at=?,
		  models_refreshed_at=?,subscription_json=?,subscription_checked_at=?
		WHERE id=?`,
		a.Email, a.Name, a.ProjectID, a.UserProjectID, rtEnc, a.ClientID, csEnc,
		a.Scope, a.UserAgent, a.ModelsJSON, a.SyncedAt, a.UpdatedAt,
		a.SyncedAt, a.SubscriptionJSON, a.SyncedAt, a.ID)
	return err
}

// UpdateAccountCredentials 只回写凭据（refresh token 轮换后），不动身份、项目、模型目录等列，
// 避免覆盖同一时刻管理端同步写入的结果。
func (s *Store) UpdateAccountCredentials(ctx context.Context, enc Encryptor, id int64, refreshToken, clientID, clientSecret string) error {
	rtEnc, err := enc.Encrypt(refreshToken)
	if err != nil {
		return err
	}
	csEnc, err := enc.Encrypt(clientSecret)
	if err != nil {
		return err
	}
	res, err := s.execCtx(ctx, `
		UPDATE accounts SET refresh_token_enc=?,client_id=?,client_secret_enc=?,updated_at=?
		WHERE id=?`, rtEnc, clientID, csEnc, nowMs(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateAccountModels 只更新账号的模型目录（含额度）和刷新时间（运行中刷新目录、后台刷新额度时用），不动凭据和身份。
func (s *Store) UpdateAccountModels(ctx context.Context, id int64, modelsJSON string) error {
	now := nowMs()
	res, err := s.execCtx(ctx, `UPDATE accounts SET models_json=?,models_refreshed_at=?,updated_at=? WHERE id=?`, modelsJSON, now, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAccountSubscription 更新账号的订阅信息和检查时间（后台定期检查时用）。
func (s *Store) SetAccountSubscription(ctx context.Context, id int64, subscriptionJSON string) error {
	now := nowMs()
	_, err := s.execCtx(ctx, `UPDATE accounts SET subscription_json=?,subscription_checked_at=?,updated_at=? WHERE id=?`,
		subscriptionJSON, now, now, id)
	return err
}

// SetAccountMaxConcurrency 设置账号单独的并发上限，0 表示跟随全局默认值。
func (s *Store) SetAccountMaxConcurrency(ctx context.Context, id int64, n int) error {
	res, err := s.execCtx(ctx, `UPDATE accounts SET max_concurrency=?,updated_at=? WHERE id=?`, n, nowMs(), id)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAccountStatus 启用/停用账号，并记录停用原因。
func (s *Store) SetAccountStatus(ctx context.Context, id int64, status, reason string) error {
	_, err := s.execCtx(ctx,
		`UPDATE accounts SET status=?,disabled_reason=?,updated_at=? WHERE id=?`,
		status, reason, nowMs(), id)
	return err
}

// idList 返回 IN 子句的占位符和参数。
func idList(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

// SetAccountsStatus 批量启用或停用账号（一条语句），清空停用原因，返回实际改动的行数。
func (s *Store) SetAccountsStatus(ctx context.Context, ids []int64, status string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	in, args := idList(ids)
	res, err := s.execCtx(ctx, `UPDATE accounts SET status=?,disabled_reason='',updated_at=? WHERE id IN (`+in+`)`,
		append([]any{status, nowMs()}, args...)...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteAccounts 批量删除账号（一条语句），返回实际删除的行数。冷却记录随外键级联删除。
func (s *Store) DeleteAccounts(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	in, args := idList(ids)
	res, err := s.execCtx(ctx, `DELETE FROM accounts WHERE id IN (`+in+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteAccount 删除账号。
func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	_, err := s.execCtx(ctx, `DELETE FROM accounts WHERE id=?`, id)
	return err
}
