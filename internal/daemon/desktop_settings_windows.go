//go:build windows

package daemon

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

const autostartRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func desktopAutostartState() (bool, string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.QUERY_VALUE)
	if err != nil {
		return false, ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("agent-memory")
	if err != nil {
		return false, ""
	}
	return v != "", v
}

func desktopSetAutostart(enabled bool, appPath string) error {
	if appPath == "" {
		return fmt.Errorf("还没有打开过桌面应用：先运行一次 agent-memory-app.exe")
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开注册表失败：%w", err)
	}
	defer k.Close()
	if !enabled {
		if err := k.DeleteValue("agent-memory"); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("删除自启失败：%w", err)
		}
		return nil
	}
	if err := k.SetStringValue("agent-memory", fmt.Sprintf(`"%s" --tray-only`, appPath)); err != nil {
		return fmt.Errorf("写入自启失败：%w", err)
	}
	return nil
}
