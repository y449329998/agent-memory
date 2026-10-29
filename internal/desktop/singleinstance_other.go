//go:build !windows

package desktop

import "errors"

var errAlready = errors.New("agent-memory 已在运行")

func acquireSingleInstance() error    { return nil }
func releaseSingleInstance()          {}
func focusExistingWindow(string) bool { return false }
