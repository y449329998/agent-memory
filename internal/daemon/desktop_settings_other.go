//go:build !windows

package daemon

import "fmt"

func desktopAutostartState() (bool, string) { return false, "" }

func desktopSetAutostart(bool, string) error {
	return fmt.Errorf("当前平台不支持开机自启")
}
