//go:build windows

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// InstallShortcuts 在桌面与开始菜单创建 agent-memory 快捷方式。
func InstallShortcuts(exePath string) error {
	paths, err := shortcutPaths()
	if err != nil {
		return err
	}
	for _, lnk := range paths {
		if err := createShortcut(lnk, exePath); err != nil {
			return fmt.Errorf("创建 %s 失败：%w", lnk, err)
		}
	}
	return nil
}

// UninstallShortcuts 删除本程序创建的快捷方式；不存在不算错误。
func UninstallShortcuts() error {
	paths, err := shortcutPaths()
	if err != nil {
		return err
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除 %s 失败：%w", p, err)
		}
	}
	return nil
}

func shortcutPaths() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return []string{
		filepath.Join(home, "Desktop", "agent-memory.lnk"),
		filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "agent-memory.lnk"),
	}, nil
}

func createShortcut(lnk, target string) error {
	quote := func(s string) string { return strings.ReplaceAll(s, `'`, `''`) }
	script := fmt.Sprintf(
		`$s=(New-Object -ComObject WScript.Shell).CreateShortcut('%s');`+
			`$s.TargetPath='%s';$s.WorkingDirectory='%s';$s.IconLocation='%s,0';$s.Save()`,
		quote(lnk), quote(target), quote(filepath.Dir(target)), quote(target))
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
