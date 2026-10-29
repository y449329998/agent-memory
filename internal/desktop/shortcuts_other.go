//go:build !windows

package desktop

import "errors"

func InstallShortcuts(string) error {
	return errors.New("当前平台不支持快捷方式")
}

func UninstallShortcuts() error {
	return errors.New("当前平台不支持快捷方式")
}
