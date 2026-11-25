package antigravity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAuthorizationURL(t *testing.T) {
	c := NewClient(Options{})
	u, err := url.Parse(c.AuthorizationURL("http://127.0.0.1:51234/oauth-callback", "st", "verifier"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != authorizationURL {
		t.Fatalf("endpoint: %s", u)
	}
	q := u.Query()
	want := map[string]string{
		"client_id":             defaultOAuthClientID,
		"redirect_uri":          "http://127.0.0.1:51234/oauth-callback",
		"response_type":         "code",
		"access_type":           "offline",
		"prompt":                "consent",
		"state":                 "st",
		"code_challenge":        CodeChallenge("verifier"),
		"code_challenge_method": "S256",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if !strings.Contains(q.Get("scope"), "cloud-platform") || !strings.Contains(q.Get("scope"), "experimentsandconfigs") {
		t.Errorf("scope: %s", q.Get("scope"))
	}
}

// 期望值用 Node 的 crypto 独立算出：SHA-256 后 base64url 编码、不带填充。
func TestCodeChallenge(t *testing.T) {
	if got := CodeChallenge("dBjftJeZ4CVP-mJ92K9ATtTmdrL7AXKuVqv8fyW8Ue0"); got != "P0pud3TJHbJy9EKxhLzPB_BuZn2Ya9DLJYffwM8SLD0" {
		t.Fatalf("challenge: %s", got)
	}
}

func TestExchangeCode(t *testing.T) {
	var form url.Values
	var ua string
	refresh := "rt-new"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form, ua = r.PostForm, r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"` + refresh + `","expires_in":3599,"scope":"openid"}`))
	}))
	defer srv.Close()
	c := NewClient(Options{TokenURL: srv.URL})

	cred, err := c.ExchangeCode(context.Background(), "", "the-code", "http://127.0.0.1:5/oauth-callback", "ver")
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "at" || cred.RefreshToken != "rt-new" || cred.ClientID != defaultOAuthClientID || cred.ExpiresAt.IsZero() {
		t.Fatalf("credential: %+v", cred)
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "the-code" || form.Get("code_verifier") != "ver" ||
		form.Get("redirect_uri") != "http://127.0.0.1:5/oauth-callback" || form.Get("client_secret") != defaultOAuthClientSecret {
		t.Fatalf("form: %v", form)
	}
	if !strings.HasPrefix(ua, "Go-http-client/") {
		t.Fatalf("user agent: %q", ua)
	}

	// 没下发 refresh token 不能导入（账号离线后就用不了）。
	refresh = ""
	if _, err := c.ExchangeCode(context.Background(), "", "c", "r", "v"); err == nil {
		t.Fatal("want error without refresh_token")
	}
}
