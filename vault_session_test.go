package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

func isolateVaultConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userConfigDir = old })
	t.Setenv("VAULT_TOKEN", "")
	return dir
}

// fakeVaultSession 提供登录、续期和 creds 三个接口，记下每次请求带的 token
func fakeVaultSession(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("X-Vault-Token"))
		mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/auth/ldap/login/"):
			fmt.Fprint(w, `{"auth":{"client_token":"vault-client","lease_duration":3600,"renewable":true}}`)
		case r.URL.Path == "/v1/auth/token/renew-self":
			fmt.Fprint(w, `{"auth":{"client_token":"vault-client","lease_duration":7200,"renewable":true}}`)
		case r.URL.Path == "/v1/creds/my-role":
			if r.Header.Get("X-Vault-Token") != "vault-client" {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"errors":["permission denied"]}`)
				return
			}
			fmt.Fprint(w, `{"data":{"service_account_token":"k8s-token"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// 输一次密码后 client token 存本机，第二次不输密码也能换 token
func TestVaultLoginCachesSession(t *testing.T) {
	isolateVaultConfig(t)
	srv, seen := fakeVaultSession(t)
	cfg := testVaultConfig(srv.URL)

	tok, err := vaultFetchToken(cfg, "pw")
	if err != nil || tok != "k8s-token" {
		t.Fatalf("第一次换 token = %q, %v", tok, err)
	}
	s := loadVaultSession(cfg)
	if s == nil || s.Token != "vault-client" {
		t.Fatalf("登录没有存下来: %+v", s)
	}
	if s.ExpiresAt.IsZero() || !s.ExpiresAt.After(time.Now()) {
		t.Errorf("到期时间不对: %v", s.ExpiresAt)
	}

	tok, err = vaultFetchTokenAuto(cfg)
	if err != nil || tok != "k8s-token" {
		t.Fatalf("免密换 token = %q, %v", tok, err)
	}
	var credsToken string
	for _, line := range seen() {
		if strings.HasPrefix(line, "/v1/creds/my-role ") {
			credsToken = strings.TrimPrefix(line, "/v1/creds/my-role ")
		}
	}
	if credsToken != "vault-client" {
		t.Errorf("creds 请求没带缓存的 client token: %q (%v)", credsToken, seen())
	}
}

// 快到期的登录会先 renew-self，把有效期续上
func TestVaultSessionRenews(t *testing.T) {
	isolateVaultConfig(t)
	srv, seen := fakeVaultSession(t)
	cfg := testVaultConfig(srv.URL)

	s := &vaultSession{Token: "vault-client", ExpiresAt: time.Now().Add(time.Hour), Renewable: true}
	if _, err := saveVaultSession(cfg, *s); err != nil {
		t.Fatal(err)
	}
	got, err := vaultSessionFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(got.ExpiresAt) < 110*time.Minute {
		t.Errorf("续期没生效: %v", got.ExpiresAt)
	}
	renewed := false
	for _, line := range seen() {
		if strings.HasPrefix(line, "/v1/auth/token/renew-self") {
			renewed = true
		}
	}
	if !renewed {
		t.Errorf("没有调用 renew-self: %v", seen())
	}
	if cached := loadVaultSession(cfg); cached == nil || time.Until(cached.ExpiresAt) < 110*time.Minute {
		t.Errorf("续期结果没写回本机: %+v", cached)
	}
}

// 没有登录、登录过期、配置对不上，都不能悄悄用一个旧 token
func TestVaultSessionRejected(t *testing.T) {
	isolateVaultConfig(t)
	srv, _ := fakeVaultSession(t)
	cfg := testVaultConfig(srv.URL)

	if _, err := vaultSessionFor(cfg); err != errNoVaultSession {
		t.Fatalf("没有登录时应返回 errNoVaultSession，got %v", err)
	}

	if _, err := saveVaultSession(cfg, vaultSession{Token: "old", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if s := loadVaultSession(cfg); s != nil {
		t.Errorf("过期的登录不该被读出来: %+v", s)
	}

	if _, err := saveVaultSession(cfg, vaultSession{Token: "old", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	other := testVaultConfig(srv.URL)
	other.Auth.Username = "bob"
	if s := loadVaultSession(other); s != nil {
		t.Errorf("换了用户名还能用旧登录: %+v", s)
	}

	t.Setenv("VAULT_TOKEN", "env-token")
	if s, err := vaultSessionFor(cfg); err != nil || s.Token != "env-token" {
		t.Errorf("VAULT_TOKEN 优先: %+v %v", s, err)
	}
}

func TestAutoDue(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		e    Entry
		want bool
	}{
		{"已过期", Entry{Token: "t", HasExp: true, Expiry: now.Add(-time.Minute)}, true},
		{"5 分钟后到期", Entry{Token: "t", HasExp: true, Expiry: now.Add(5 * time.Minute)}, true},
		{"还早", Entry{Token: "t", HasExp: true, Expiry: now.Add(time.Hour)}, false},
		{"不是 JWT", Entry{Token: "abc"}, false},
		{"没有 token", Entry{}, true},
		{"exec 自己续", Entry{Token: "t", HasExp: true, Expiry: now.Add(-time.Minute), HasExec: true}, false},
	}
	for _, c := range cases {
		if got := autoDue(c.e, now); got != c.want {
			t.Errorf("%s: autoDue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAutoTargetEntry(t *testing.T) {
	cfg := &vaultConfig{}
	entries := []Entry{{File: "/tmp/a", User: "alice"}, {File: "/tmp/a", User: "bob", IsCurrent: true}}
	if e, ok := autoTargetEntry(entries, cfg); !ok || e.User != "bob" {
		t.Errorf("默认应选当前 context: %+v %v", e, ok)
	}
	cfg.Target = vaultTarget{User: "alice"}
	if e, ok := autoTargetEntry(entries, cfg); !ok || e.User != "alice" {
		t.Errorf("配置了 target 应选它: %+v %v", e, ok)
	}
	if _, ok := autoTargetEntry(nil, cfg); ok {
		t.Error("没有条目时不该有目标")
	}
}

// TUI 里按 a 打开自动更新：写进配置，列表上显示 [开]
func TestToggleAutoUpdate(t *testing.T) {
	isolateVaultConfig(t)
	cfg := testVaultConfig("https://vault.example.com")
	if _, err := saveVaultConfig(cfg); err != nil {
		t.Fatal(err)
	}

	m := model{cfg: cfg, entries: []Entry{{File: "/tmp/config", User: "alice", Token: "not-a-jwt"}}}
	updated, _ := m.toggleAuto()
	got := updated.(model)
	if !got.cfg.AutoUpdate {
		t.Fatal("开关没打开")
	}
	reloaded, err := loadVaultConfig()
	if err != nil || reloaded == nil || !reloaded.AutoUpdate {
		t.Fatalf("开关没写进配置: %+v %v", reloaded, err)
	}

	view := ansiEscape.ReplaceAllString(got.viewList(), "")
	if !strings.Contains(view, "自动更新 [开]") {
		t.Errorf("列表没显示开关状态:\n%s", view)
	}
	if !strings.Contains(view, "按 v 登录一次 Vault") {
		t.Errorf("没有登录时应提示先登录:\n%s", view)
	}
}
