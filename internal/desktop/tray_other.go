//go:build !windows

package desktop

import (
	"errors"
	"log"
)

type trayActions struct {
	OpenWindow  func()
	Sync        func()
	OpenLog     func()
	AutostartOn func() bool
	ToggleAuto  func(bool) error
	StopAndQuit func()
}

func startTray(trayActions, *log.Logger) (<-chan struct{}, <-chan struct{}, error) {
	return nil, nil, errors.New("当前平台不支持系统托盘")
}
