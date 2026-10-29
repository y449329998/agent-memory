//go:build !windows

package desktop

import "errors"

func setAutostart(bool, string) error {
	return errors.New("当前平台不支持开机自启")
}

func autostartEnabled() (bool, string) { return false, "" }
