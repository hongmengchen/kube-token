package main

import (
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
