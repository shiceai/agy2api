package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/store"
)

// 代理管理：添加（每行一个）、改名或换地址、启停、删除、测试（出口和延迟），账号绑定代理。代理地址含密码，
// 接口只返回协议、host:port 和用户名，日志只写代理 ID 和名称。

const (
	// proxyBatchLimit 是一次最多添加的代理数。
	proxyBatchLimit = 500
	// proxyCheckParallel 是同时测试的代理数。
	proxyCheckParallel = 8
)

// proxyAccount 是绑定在代理上的账号。
type proxyAccount struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
}

// proxyView 是管理端看到的代理，不含密码。
type proxyView struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Scheme   string `json:"scheme"`
	Host     string `json:"host"` // host:port
	Username string `json:"username,omitempty"`
	Status   string `json:"status"`
	// Egress* 是最近一次测试拿到的出口（IP、国家两位代码、地区、城市、运营商）；ConnectMs 是服务器和代理建立 TCP
	// 连接的耗时，LatencyMs 是经代理连上 Google 后复用连接的往返，0 表示没测出来；CheckError 是最近一次测试的失败原因。
	EgressIP        string         `json:"egress_ip"`
	EgressCountry   string         `json:"egress_country"`
	EgressRegion    string         `json:"egress_region"`
	EgressCity      string         `json:"egress_city"`
	EgressOrg       string         `json:"egress_org"`
	ConnectMs       int64          `json:"connect_ms"`
	LatencyMs       int64          `json:"latency_ms"`
	PrevEgressIP    string         `json:"prev_egress_ip,omitempty"`
	EgressChangedAt int64          `json:"egress_changed_at"`
	CheckedAt       int64          `json:"checked_at"`
	CheckError      string         `json:"check_error,omitempty"`
	Accounts        []proxyAccount `json:"accounts"`
	CreatedAt       int64          `json:"created_at"`
}

func toProxyView(p *store.Proxy, accounts []proxyAccount) proxyView {
	v := proxyView{ID: p.ID, Name: p.Name, Status: p.Status, EgressIP: p.EgressIP, EgressCountry: p.EgressCountry,
		EgressRegion: p.EgressRegion, EgressCity: p.EgressCity, EgressOrg: p.EgressOrg, ConnectMs: p.ConnectMs, LatencyMs: p.LatencyMs,
		PrevEgressIP: p.PrevEgressIP, EgressChangedAt: p.EgressChangedAt, CheckedAt: p.CheckedAt,
		CheckError: p.CheckError, Accounts: accounts, CreatedAt: p.CreatedAt}
	if v.Accounts == nil {
		v.Accounts = []proxyAccount{}
	}
	if u, err := url.Parse(p.URL); err == nil {
		v.Scheme, v.Host = u.Scheme, u.Host
		if u.User != nil {
			v.Username = u.User.Username()
		}
	}
	return v
}

// proxyLabel 是日志和提示里用的代理名称（不含密码）。
func proxyLabel(p *store.Proxy) string {
	if p.Name != "" {
		return p.Name
	}
	return fmt.Sprintf("#%d", p.ID)
}

// proxiesView 返回全部代理和各自绑定的账号。
func (s *Service) proxiesView(ctx context.Context) ([]proxyView, error) {
	proxies, err := s.store.ListProxies(ctx, s.enc)
	if err != nil {
		return nil, err
	}
	accounts, err := s.store.ListAccounts(ctx, s.enc)
	if err != nil {
		return nil, err
	}
	bound := map[int64][]proxyAccount{}
	for _, a := range accounts {
		if a.ProxyID != 0 {
			bound[a.ProxyID] = append(bound[a.ProxyID], proxyAccount{ID: a.ID, Email: a.Email})
		}
	}
	out := make([]proxyView, 0, len(proxies))
	for _, p := range proxies {
		out = append(out, toProxyView(p, bound[p.ID]))
	}
	return out, nil
}

func (s *Service) writeProxies(w http.ResponseWriter, r *http.Request) {
	list, err := s.proxiesView(r.Context())
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proxies": list})
}

func (s *Service) adminListProxies(w http.ResponseWriter, r *http.Request) {
	s.writeProxies(w, r)
}

// errProxyFormat 是认不出代理写法时的说明。
var errProxyFormat = errors.New("格式不认识，可用 scheme://user:pass@host:port，或不带协议的 user:pass@host:port、" +
	"host:port:user:pass、user:pass:host:port、host:port")

// normalizeProxy 把一行输入整理成代理地址。完整地址写作 scheme://[user:pass@]host:port（协议见
// antigravity.ProxySchemes）；不带协议的按 scheme，认代理商常见的几种写法：
//   - user:pass@host:port，以及反过来的 host:port@user:pass（按最后一个 @ 切开，密码里可以有 @）
//   - host:port:user:pass，以及 user:pass:host:port（看第 2 段还是第 4 段是端口）
//   - host:port
func normalizeProxy(line, scheme string) (string, error) {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, "://") {
		u, err := proxyWithoutScheme(line, scheme)
		if err != nil {
			return "", err
		}
		line = u.String()
	}
	u, err := antigravity.ParseProxy(line)
	if err != nil {
		return "", err
	}
	u.Path = ""
	return u.String(), nil
}

func proxyWithoutScheme(line, scheme string) (*url.URL, error) {
	var hostport, userinfo string
	if i := strings.LastIndex(line, "@"); i >= 0 {
		left, right := line[:i], line[i+1:]
		switch {
		case isHostPort(right):
			hostport, userinfo = right, left
		case isHostPort(left):
			hostport, userinfo = left, right
		default:
			return nil, errProxyFormat
		}
	} else {
		parts := strings.Split(line, ":")
		switch {
		case len(parts) == 2:
			hostport = line
		case len(parts) == 4 && isPort(parts[1]):
			hostport, userinfo = parts[0]+":"+parts[1], parts[2]+":"+parts[3]
		case len(parts) == 4 && isPort(parts[3]):
			hostport, userinfo = parts[2]+":"+parts[3], parts[0]+":"+parts[1]
		default:
			return nil, errProxyFormat
		}
	}
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || host == "" || !isPort(port) {
		return nil, errProxyFormat
	}
	u := &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port)}
	if userinfo != "" {
		if user, pass, ok := strings.Cut(userinfo, ":"); ok {
			u.User = url.UserPassword(user, pass)
		} else {
			u.User = url.User(userinfo)
		}
	}
	return u, nil
}

func isPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && n < 65536
}

func isHostPort(s string) bool {
	host, port, err := net.SplitHostPort(s)
	return err == nil && host != "" && isPort(port)
}

// proxyAddResult 是添加代理时一行的结果。
type proxyAddResult struct {
	Line    string `json:"line"`   // 输入的这一行，密码已遮住
	Status  string `json:"status"` // added / duplicate / invalid
	Message string `json:"message,omitempty"`
	ID      int64  `json:"id,omitempty"`
}

// maskLine 遮住一行输入里的密码，回显用。
func maskLine(line, normalized string) string {
	if u, err := url.Parse(normalized); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			return strings.ReplaceAll(line, pw, "***")
		}
	}
	return line
}

// adminAddProxies 添加代理，每行一个；和已有代理地址相同的跳过。不测试，由管理端接着调测试接口。
func (s *Service) adminAddProxies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text   string `json:"text"`
		Scheme string `json:"scheme"`
	}
	if !decodeAdminJSON(w, r, 1<<20, &req) {
		return
	}
	scheme := req.Scheme
	if scheme == "" {
		scheme = "http"
	}
	if _, err := antigravity.ParseProxy(scheme + "://h:1"); err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return
	}
	var lines []string
	for _, l := range strings.Split(req.Text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 || len(lines) > proxyBatchLimit {
		adminError(w, http.StatusBadRequest, fmt.Sprintf("每次添加 1～%d 个代理", proxyBatchLimit))
		return
	}
	existing, err := s.store.ListProxies(r.Context(), s.enc)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	seen := map[string]bool{}
	for _, p := range existing {
		seen[p.URL] = true
	}
	results := make([]proxyAddResult, 0, len(lines))
	for _, line := range lines {
		norm, err := normalizeProxy(line, scheme)
		if err != nil {
			results = append(results, proxyAddResult{Line: line, Status: "invalid", Message: err.Error()})
			continue
		}
		res := proxyAddResult{Line: maskLine(line, norm)}
		if seen[norm] {
			res.Status, res.Message = "duplicate", "已有同样的代理"
			results = append(results, res)
			continue
		}
		u, _ := url.Parse(norm)
		p := &store.Proxy{Name: u.Host, URL: norm}
		if err := s.store.CreateProxy(r.Context(), s.enc, p); err != nil {
			adminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		seen[norm] = true
		res.Status, res.ID = "added", p.ID
		results = append(results, res)
	}
	slog.InfoContext(r.Context(), "已添加代理", "lines", len(lines))
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// adminUpdateProxy 改代理名称；url 不为空时换地址（测试结果清空，绑定它的账号之后走新地址）。
func (s *Service) adminUpdateProxy(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		Scheme string `json:"scheme"`
	}
	if !decodeAdminJSON(w, r, 1<<12, &req) {
		return
	}
	cur, err := s.store.GetProxy(r.Context(), s.enc, id)
	if errors.Is(err, store.ErrNotFound) {
		adminError(w, http.StatusNotFound, "代理不存在")
		return
	}
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 64 {
		adminError(w, http.StatusBadRequest, "名称最多 64 个字符")
		return
	}
	addr := cur.URL
	if strings.TrimSpace(req.URL) != "" {
		scheme := req.Scheme
		if scheme == "" {
			scheme = "http"
		}
		if addr, err = normalizeProxy(req.URL, scheme); err != nil {
			adminError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if name == "" {
		u, _ := url.Parse(addr)
		name = u.Host
	}
	if err := s.store.UpdateProxy(r.Context(), s.enc, id, name, addr); err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已修改代理", "proxy_id", id, "name", name, "address_changed", addr != cur.URL)
	if addr != cur.URL {
		s.reloadAccounts(r.Context())
		s.client.ForgetProxy(cur.URL)
	}
	s.writeProxies(w, r)
}

// adminDeleteProxy 删除代理；还有账号绑定时拒绝（账号会改成直连），要先改绑。
func (s *Service) adminDeleteProxy(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// 先取出地址：删除成功后要回收这个地址的连接池。
	cur, err := s.store.GetProxy(r.Context(), s.enc, id)
	if err == nil {
		err = s.store.DeleteProxy(r.Context(), id)
	}
	switch {
	case errors.Is(err, store.ErrInUse):
		adminError(w, http.StatusConflict, "还有账号绑定这个代理，先给它们换代理或改成直连")
		return
	case errors.Is(err, store.ErrNotFound):
		adminError(w, http.StatusNotFound, "代理不存在")
		return
	case err != nil:
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已删除代理", "proxy_id", id)
	s.client.ForgetProxy(cur.URL)
	s.writeProxies(w, r)
}

// adminCheckProxies 测试代理（出口信息和到 Google 的延迟，ids 为空时测试全部），返回测试后的代理列表。
func (s *Service) adminCheckProxies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeAdminJSON(w, r, 1<<16, &req) {
		return
	}
	proxies, err := s.store.ListProxies(r.Context(), s.enc)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(req.IDs) > 0 {
		want := map[int64]bool{}
		for _, id := range req.IDs {
			want[id] = true
		}
		picked := proxies[:0]
		for _, p := range proxies {
			if want[p.ID] {
				picked = append(picked, p)
			}
		}
		proxies = picked
	}
	s.checkProxies(r.Context(), proxies)
	s.writeProxies(w, r)
}

// checkProxies 并发测试代理（出口和延迟）并记下结果。
func (s *Service) checkProxies(ctx context.Context, proxies []*store.Proxy) {
	sem := make(chan struct{}, proxyCheckParallel)
	var wg sync.WaitGroup
	for _, p := range proxies {
		wg.Add(1)
		sem <- struct{}{}
		go func(p *store.Proxy) {
			defer wg.Done()
			defer func() { <-sem }()
			c := testProxy(ctx, p.URL)
			if c.Error != "" {
				slog.WarnContext(ctx, "代理测试没通过", "proxy_id", p.ID, "name", proxyLabel(p), "err", c.Error)
			}
			if c.IP != "" && p.EgressIP != "" && p.EgressIP != c.IP {
				slog.WarnContext(ctx, "代理出口变了", "proxy_id", p.ID, "name", proxyLabel(p), "from", p.EgressIP, "to", c.IP)
			}
			wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := s.store.SetProxyCheck(wctx, p.ID, c); err != nil {
				slog.ErrorContext(wctx, "保存代理测试结果失败", "proxy_id", p.ID, "err", err)
			}
		}(p)
	}
	wg.Wait()
}

// adminSetProxiesStatus 启用或停用代理（ids 一个或多个）。停用的代理不参与自动分配，绑定它的账号暂停调度，
// 不会改成直连。
func (s *Service) adminSetProxiesStatus(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []int64 `json:"ids"`
		}
		if !decodeAdminJSON(w, r, 1<<16, &req) {
			return
		}
		if len(req.IDs) == 0 || len(req.IDs) > 1000 {
			adminError(w, http.StatusBadRequest, "ids must have 1-1000 items")
			return
		}
		n, err := s.store.SetProxiesStatus(r.Context(), req.IDs, status)
		if err != nil {
			adminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		slog.InfoContext(r.Context(), "已修改代理状态", "proxies", n, "status", status)
		s.reloadAccounts(r.Context())
		s.writeProxies(w, r)
	}
}

// 账号绑定代理。

// proxyChoice 是添加账号或批量分配时选的代理：auto 自动分配空闲代理，none 直连，proxy 用 proxy_id。
type proxyChoice struct {
	Mode    string `json:"mode"`
	ProxyID int64  `json:"proxy_id"`
}

var errNoFreeProxy = errors.New("没有可用的空闲代理（启用、没绑账号、最近测试没失败的），先添加代理，或改选直连、指定代理")

// proxyFailure 是连不上账号代理时给管理端看的说明。
func proxyFailure(err error) string {
	return "连不上账号绑定的代理，没有改成直连：" + proxyCause(err)
}

// proxyCause 取代理故障最里面的原因（如 "dial tcp …: connection refused"），不含请求地址和代理密码。
func proxyCause(err error) string {
	var pe *antigravity.ProxyError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err.Error()
	}
	return err.Error()
}

// freeProxies 返回能自动分配的代理：启用、没有账号绑定、最近一次测试没失败的（没测试过的也算）。
func (s *Service) freeProxies(ctx context.Context) ([]*store.Proxy, error) {
	proxies, err := s.store.ListProxies(ctx, s.enc)
	if err != nil {
		return nil, err
	}
	accounts, err := s.store.ListAccounts(ctx, s.enc)
	if err != nil {
		return nil, err
	}
	used := map[int64]bool{}
	for _, a := range accounts {
		used[a.ProxyID] = true
	}
	var free []*store.Proxy
	for _, p := range proxies {
		if !used[p.ID] && p.CheckError == "" && p.Status == store.StatusEnabled {
			free = append(free, p)
		}
	}
	return free, nil
}

// resolveProxyChoice 把添加账号时的代理选择换成代理（nil 表示直连）。
func (s *Service) resolveProxyChoice(ctx context.Context, c *proxyChoice) (*store.Proxy, error) {
	if c == nil {
		return nil, nil
	}
	switch c.Mode {
	case "", "none":
		return nil, nil
	case "proxy":
		p, err := s.store.GetProxy(ctx, s.enc, c.ProxyID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("选的代理不存在")
		}
		if err == nil && p.Status != store.StatusEnabled {
			return nil, errors.New("选的代理已停用，先启用或改选别的")
		}
		return p, err
	case "auto":
		free, err := s.freeProxies(ctx)
		if err != nil {
			return nil, err
		}
		if len(free) == 0 {
			return nil, errNoFreeProxy
		}
		return free[0], nil
	}
	return nil, fmt.Errorf("代理选择 %q 不认识", c.Mode)
}

// adminSetAccountProxy 改一个账号绑定的代理，proxy_id 为 0 表示直连。
func (s *Service) adminSetAccountProxy(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		ProxyID int64 `json:"proxy_id"`
	}
	if !decodeAdminJSON(w, r, 1<<10, &req) {
		return
	}
	if !s.setAccountsProxy(w, r, map[int64]int64{id: req.ProxyID}) {
		return
	}
	a, err := s.store.GetAccount(r.Context(), s.enc, id)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": s.toAccountView(r.Context(), a)})
}

// adminAssignProxies 批量给账号分配代理：auto 给其中没绑代理的账号各分配一个空闲代理（空闲的不够时整批不改），
// none 全部改成直连，proxy 全部绑到 proxy_id。
func (s *Service) adminAssignProxies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
		proxyChoice
	}
	if !decodeAdminJSON(w, r, 1<<16, &req) {
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 1000 {
		adminError(w, http.StatusBadRequest, "ids must have 1-1000 items")
		return
	}
	assign := map[int64]int64{}
	skipped := 0
	switch req.Mode {
	case "none":
		for _, id := range req.IDs {
			assign[id] = 0
		}
	case "proxy":
		for _, id := range req.IDs {
			assign[id] = req.ProxyID
		}
	case "auto":
		accounts, err := s.store.ListAccounts(r.Context(), s.enc)
		if err != nil {
			adminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		want := map[int64]bool{}
		for _, id := range req.IDs {
			want[id] = true
		}
		var need []int64
		for _, a := range accounts {
			if !want[a.ID] {
				continue
			}
			if a.ProxyID != 0 {
				skipped++
				continue
			}
			need = append(need, a.ID)
		}
		free, err := s.freeProxies(r.Context())
		if err != nil {
			adminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(free) < len(need) {
			adminError(w, http.StatusBadRequest, fmt.Sprintf("空闲的代理只有 %d 个，选中的账号里有 %d 个没绑代理", len(free), len(need)))
			return
		}
		for i, id := range need {
			assign[id] = free[i].ID
		}
	default:
		adminError(w, http.StatusBadRequest, "mode must be auto, none or proxy")
		return
	}
	if len(assign) > 0 && !s.setAccountsProxy(w, r, assign) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assigned": len(assign), "skipped": skipped})
}

// setAccountsProxy 落库账号绑定并刷新号池；失败时已写好错误响应，返回 false。
func (s *Service) setAccountsProxy(w http.ResponseWriter, r *http.Request, assign map[int64]int64) bool {
	for _, proxyID := range assign {
		if proxyID == 0 {
			continue
		}
		p, err := s.store.GetProxy(r.Context(), s.enc, proxyID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				adminError(w, http.StatusBadRequest, "代理不存在")
			} else {
				adminError(w, http.StatusInternalServerError, err.Error())
			}
			return false
		}
		if p.Status != store.StatusEnabled {
			adminError(w, http.StatusBadRequest, "代理 "+proxyLabel(p)+" 已停用，先启用或改选别的")
			return false
		}
	}
	err := s.store.SetAccountsProxy(r.Context(), assign)
	if errors.Is(err, store.ErrNotFound) {
		adminError(w, http.StatusNotFound, "account not found")
		return false
	}
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	slog.InfoContext(r.Context(), "已修改账号绑定的代理", "accounts", len(assign))
	s.reloadAccounts(r.Context())
	return true
}
