//go:build windows

package desktop

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

const autostartRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// setAutostart 写/删 HKCU Run；值与 --tray-only 启动方式一致。
func setAutostart(enabled bool, exePath string) error {
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
	value := fmt.Sprintf(`"%s" --tray-only`, exePath)
	if err := k.SetStringValue("agent-memory", value); err != nil {
		return fmt.Errorf("写入自启失败：%w", err)
	}
	return nil
}

func autostartEnabled() (bool, string) {
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
