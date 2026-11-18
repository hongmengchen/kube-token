package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateStatus(t *testing.T) {
	m := newModel(nil, nil)
	if !strings.Contains(m.updateStatus(), "检查更新中") {
		t.Fatalf("初始状态 = %q", m.updateStatus())
	}

	m.latest = "9.9.9"
	if !m.canUpdate() {
		t.Fatal("最新版更新时应当可以升级")
	}
	if got := m.updateStatus(); !strings.Contains(got, "按 U 升级") {
		t.Errorf("可升级状态 = %q", got)
	}

	m.updatedTo = "9.9.9"
	if m.canUpdate() {
		t.Error("升级完不应再提示升级")
	}
	if got := m.updateStatus(); !strings.Contains(got, "重开生效") {
		t.Errorf("升级后状态 = %q", got)
	}
}

func TestFindChecksum(t *testing.T) {
	sums := "aaa  kube-token_linux_amd64\n" +
		"bbb  *kube-token_windows_arm64.exe\n" +
		"ccc  other-file\n"
	got, err := findChecksum(strings.NewReader(sums), "kube-token_windows_arm64.exe")
	if err != nil {
		t.Fatal(err)
	}
	if got != "bbb" {
		t.Fatalf("checksum = %q", got)
	}
	if _, err := findChecksum(strings.NewReader(sums), "kube-token_darwin_arm64"); err == nil {
		t.Fatal("缺条目时应当报错")
	}
}

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bin")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("hello"))
	want := hex.EncodeToString(sum[:])
	if err := verifyChecksum(file, want); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(file, strings.Repeat("0", 64)); err == nil {
		t.Fatal("校验和不一致时应当报错")
	}
}
