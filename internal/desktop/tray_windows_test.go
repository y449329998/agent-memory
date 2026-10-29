//go:build windows

package desktop

import (
	"testing"
	"unsafe"
)

func TestNotifyIconDataLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("布局常量按 64 位 Windows 校验")
	}
	if got := unsafe.Sizeof(notifyIconData{}); got != 976 {
		t.Fatalf("notifyIconData 大小 = %d，期望 976（NOTIFYICONDATAW V3）", got)
	}
}

func TestTrayMenuEvent(t *testing.T) {
	cases := []struct {
		id   int
		want trayEvent
		ok   bool
	}{
		{trayCmdOpen, trayEvOpen, true},
		{trayCmdSync, trayEvSync, true},
		{trayCmdLog, trayEvLog, true},
		{trayCmdAutostart, trayEvToggleAuto, true},
		{trayCmdStop, trayEvStop, true},
		{1000, 0, false},
		{wmTrayMsg, 0, false},
	}
	for _, c := range cases {
		got, ok := trayMenuEvent(c.id)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("trayMenuEvent(%d) = %v,%v，期望 %v,%v", c.id, got, ok, c.want, c.ok)
		}
	}
}
