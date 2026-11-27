package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ErrInUse 表示记录还在被引用（如代理还有账号绑定），不能删除。
var ErrInUse = errors.New("store: in use")

// Proxy 是一个代理。URL 是明文（已解密），含用户名密码，不能写进日志或返回给接口。
type Proxy struct {
	ID   int64
	Name string
	URL  string
	// Status 是 enabled / disabled：停用的不参与自动分配，绑定它的账号暂停调度。
	Status string
	// EgressIP、EgressCountry 等是最近一次测试拿到的出口：IP、国家（两位代码）、地区、城市、运营商；
	// CheckError 是最近一次测试的失败原因；ConnectMs 是服务器和代理建立 TCP 连接的耗时，LatencyMs 是经代理连上 Google 后
	// 在同一连接上再请求一次的往返（都是毫秒，0 表示没测出来）。
	EgressIP      string
	EgressCountry string
	EgressRegion  string
	EgressCity    string
	EgressOrg     string
	ConnectMs     int64
	LatencyMs     int64
	// PrevEgressIP、EgressChangedAt：检测到出口和上次不同时记下原来的 IP 和时间。
	PrevEgressIP    string
	EgressChangedAt int64
	CheckedAt       int64
	CheckError      string
	CreatedAt       int64
	UpdatedAt       int64
}

const proxyCols = `id,name,url_enc,status,egress_ip,egress_country,egress_region,egress_city,egress_org,connect_ms,latency_ms,
	prev_egress_ip,egress_changed_at,checked_at,check_error,created_at,updated_at`

func scanProxy(enc Encryptor, row interface{ Scan(...any) error }) (*Proxy, error) {
	var p Proxy
	var urlEnc string
	if err := row.Scan(&p.ID, &p.Name, &urlEnc, &p.Status, &p.EgressIP, &p.EgressCountry, &p.EgressRegion, &p.EgressCity,
		&p.EgressOrg, &p.ConnectMs, &p.LatencyMs, &p.PrevEgressIP, &p.EgressChangedAt, &p.CheckedAt, &p.CheckError, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	var err error
	if p.URL, err = enc.Decrypt(urlEnc); err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateProxy 新增一个代理，回填 ID 和时间。
func (s *Store) CreateProxy(ctx context.Context, enc Encryptor, p *Proxy) error {
	urlEnc, err := enc.Encrypt(p.URL)
	if err != nil {
		return err
	}
	now := nowMs()
	res, err := s.execCtx(ctx, `INSERT INTO proxies (name,url_enc,created_at,updated_at) VALUES (?,?,?,?)`,
		p.Name, urlEnc, now, now)
	if err != nil {
		return err
	}
	if p.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	p.CreatedAt, p.UpdatedAt, p.Status = now, now, StatusEnabled
	return nil
}

// UpdateProxy 修改代理的名称和地址；地址改了时清掉检测结果（换了出口），需要重新检测。
func (s *Store) UpdateProxy(ctx context.Context, enc Encryptor, id int64, name, url string) error {
	urlEnc, err := enc.Encrypt(url)
	if err != nil {
		return err
	}
	cur, err := s.GetProxy(ctx, enc, id)
	if err != nil {
		return err
	}
	if cur.URL == url {
		_, err = s.execCtx(ctx, `UPDATE proxies SET name=?, updated_at=? WHERE id=?`, name, nowMs(), id)
		return err
	}
	_, err = s.execCtx(ctx, `
		UPDATE proxies SET name=?, url_enc=?, egress_ip='', egress_country='', egress_region='', egress_city='', egress_org='',
		  connect_ms=0, latency_ms=0, prev_egress_ip='', egress_changed_at=0, checked_at=0, check_error='', updated_at=? WHERE id=?`,
		name, urlEnc, nowMs(), id)
	return err
}

// GetProxy 按 ID 读取代理。
func (s *Store) GetProxy(ctx context.Context, enc Encryptor, id int64) (*Proxy, error) {
	p, err := scanProxy(enc, s.reader.QueryRowContext(ctx, `SELECT `+proxyCols+` FROM proxies WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ListProxies 返回全部代理，按 ID 升序。
func (s *Store) ListProxies(ctx context.Context, enc Encryptor) ([]*Proxy, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT `+proxyCols+` FROM proxies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Proxy
	for rows.Next() {
		p, err := scanProxy(enc, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteProxy 删除代理；还有账号绑定时返回 ErrInUse（账号会悄悄改成直连，必须先改绑）。
func (s *Store) DeleteProxy(ctx context.Context, id int64) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE proxy_id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM proxies WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ProxyCheck 是一次代理测试的结果。IP 为空表示没拿到出口（连不上代理）；Error 不为空表示测试没通过
// （连不上代理，或出口拿到了但经代理连不上 Google）。ConnectMs、LatencyMs 见 Proxy，为 0 表示没测出来。
type ProxyCheck struct {
	IP, Country, Region, City, Org string
	ConnectMs, LatencyMs           int64
	Error                          string
}

// SetProxyCheck 记下一次测试的结果。拿到了出口就更新出口信息，出口和上次不同时记下原来的 IP 和时间；
// 没拿到出口时保留上次的出口，只记失败原因，延迟清零。
func (s *Store) SetProxyCheck(ctx context.Context, id int64, c ProxyCheck) error {
	now := nowMs()
	if c.IP == "" {
		_, err := s.execCtx(ctx, `UPDATE proxies SET checked_at=?, check_error=?, connect_ms=0, latency_ms=0 WHERE id=?`, now, c.Error, id)
		return err
	}
	_, err := s.execCtx(ctx, `
		UPDATE proxies SET
		  prev_egress_ip    = CASE WHEN egress_ip != '' AND egress_ip != ?1 THEN egress_ip ELSE prev_egress_ip END,
		  egress_changed_at = CASE WHEN egress_ip != '' AND egress_ip != ?1 THEN ?3 ELSE egress_changed_at END,
		  egress_ip = ?1, egress_country = ?2, checked_at = ?3, check_error = ?4,
		  egress_region = ?5, egress_city = ?6, egress_org = ?7, connect_ms = ?8, latency_ms = ?9
		WHERE id = ?10`, c.IP, c.Country, now, c.Error, c.Region, c.City, c.Org, c.ConnectMs, c.LatencyMs, id)
	return err
}

// SetProxiesStatus 一条语句改若干个代理的启停，返回实际改动的条数。
func (s *Store) SetProxiesStatus(ctx context.Context, ids []int64, status string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	args := []any{status, nowMs()}
	marks := make([]string, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args = append(args, id)
	}
	res, err := s.execCtx(ctx, `UPDATE proxies SET status=?, updated_at=? WHERE id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetAccountsProxy 在一个事务里改账号绑定的代理：键是账号 ID，值是代理 ID（0 表示直连）。
func (s *Store) SetAccountsProxy(ctx context.Context, assign map[int64]int64) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := nowMs()
	for accountID, proxyID := range assign {
		res, err := tx.ExecContext(ctx, `UPDATE accounts SET proxy_id=?, updated_at=? WHERE id=?`, nullID(proxyID), now, accountID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	}
	return tx.Commit()
}

// nullID 把 0 换成 NULL（外键列）。
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
