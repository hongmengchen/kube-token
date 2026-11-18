package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Vault 登录一次后会拿到 client token（默认有效期以天计，还能续期）。
// 把它存在本机配置目录（0600）里，下次读 creds 就不用再输密码；
// 用户密码本身从不落盘。
type vaultSession struct {
	Token     string    `json:"token"`
	Addr      string    `json:"addr"`
	Namespace string    `json:"namespace,omitempty"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Username  string    `json:"username,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Renewable bool      `json:"renewable,omitempty"`
}

var errNoVaultSession = errors.New("还没有可用的 Vault 登录")

// 提前这么久续期，别等真的过期
const vaultRenewWindow = 24 * time.Hour

func vaultSessionPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kube-token", "session.json"), nil
}

// identity 把登录凭据和产生它的配置绑在一起，配置改了旧凭据就作废
func (s vaultSession) identity(cfg *vaultConfig) vaultSession {
	s.Addr = cfg.Addr
	s.Namespace = cfg.Namespace
	s.Method = cfg.Auth.Method
	s.Path = cfg.Auth.Path
	s.Username = cfg.Auth.Username
	return s
}

func (s vaultSession) matches(cfg *vaultConfig) bool {
	return s.Addr != "" && s.Addr == cfg.Addr &&
		s.Namespace == cfg.Namespace &&
		s.Method == cfg.Auth.Method &&
		s.Path == cfg.Auth.Path &&
		s.Username == cfg.Auth.Username
}

func (s vaultSession) expired(now time.Time) bool {
	return !s.ExpiresAt.IsZero() && !s.ExpiresAt.After(now)
}

// loadVaultSession 读本机保存的登录；没有、配置对不上或已过期都返回 nil
func loadVaultSession(cfg *vaultConfig) *vaultSession {
	if cfg == nil {
		return nil
	}
	path, err := vaultSessionPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s vaultSession
	if err := json.Unmarshal(data, &s); err != nil || strings.TrimSpace(s.Token) == "" {
		return nil
	}
	if !s.matches(cfg) || s.expired(time.Now()) {
		return nil
	}
	return &s
}

func saveVaultSession(cfg *vaultConfig, s vaultSession) (string, error) {
	path, err := vaultSessionPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	s = s.identity(cfg)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	return path, nil
}

// clearVaultSession 删掉本机保存的登录（logout / 登录失效时）
func clearVaultSession() error {
	path, err := vaultSessionPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// sessionFromAuth 解析登录/续期响应里的 auth 段
func sessionFromAuth(auth map[string]any) vaultSession {
	var s vaultSession
	if v, ok := auth["client_token"].(string); ok {
		s.Token = strings.TrimSpace(v)
	}
	if d, ok := auth["lease_duration"].(float64); ok && d > 0 {
		s.ExpiresAt = time.Now().Add(time.Duration(d) * time.Second)
	}
	s.Renewable, _ = auth["renewable"].(bool)
	return s
}

// vaultSessionFor 找一份不用密码就能用的登录：VAULT_TOKEN 环境变量优先，
// 其次是本机保存的 client token；快过期时先 renew-self 续上。
func vaultSessionFor(cfg *vaultConfig) (vaultSession, error) {
	if tok := strings.TrimSpace(os.Getenv("VAULT_TOKEN")); tok != "" {
		return vaultSession{Token: tok}.identity(cfg), nil
	}

	s := loadVaultSession(cfg)
	if s == nil {
		return vaultSession{}, errNoVaultSession
	}
	if s.Renewable && !s.ExpiresAt.IsZero() && time.Until(s.ExpiresAt) < vaultRenewWindow {
		if err := renewVaultSession(cfg, s); err == nil {
			_, _ = saveVaultSession(cfg, *s)
		}
		// 续期失败但 token 还没到期就照常用；到期的会在 loadVaultSession 里被挡掉
	}
	return *s, nil
}

// renewVaultSession 用已有 client token 调 renew-self，延长有效期
func renewVaultSession(cfg *vaultConfig, s *vaultSession) error {
	out, err := cfg.request(http.MethodPost, "auth/token/renew-self", s.Token, map[string]any{})
	if err != nil {
		return err
	}
	auth, _ := out["auth"].(map[string]any)
	fresh := sessionFromAuth(auth)
	if fresh.Token != "" {
		s.Token = fresh.Token
	}
	if !fresh.ExpiresAt.IsZero() {
		s.ExpiresAt = fresh.ExpiresAt
	}
	s.Renewable = fresh.Renewable
	return nil
}

func isVaultAuthError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Vault 返回 403")
}

// vaultFetchTokenAuto 不需要密码：用 VAULT_TOKEN 或本机保存的登录读 creds
func vaultFetchTokenAuto(cfg *vaultConfig) (string, error) {
	if err := cfg.validate(); err != nil {
		return "", err
	}
	s, err := vaultSessionFor(cfg)
	if err != nil {
		return "", err
	}
	tok, err := vaultFetchTokenWithSession(cfg, s)
	if err != nil && isVaultAuthError(err) {
		_ = clearVaultSession()
		return "", fmt.Errorf("%w；Vault 登录可能已失效，请在 TUI 里按 v 重新输入密码", err)
	}
	return tok, err
}
