//go:build !windows

package desktop

func showAppWindow() {
	requestWindow()
}

func closeAppWindow() {}

func startShowListener() {}

func signalShowWindow() bool { return false }
