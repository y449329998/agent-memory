//go:build !windows

package desktop

// screenSize 在非 Windows 上没有零依赖的屏幕尺寸读法，返回 0 表示未知，
// 调用方据此跳过窗口位置裁剪，直接用默认尺寸。
func screenSize() (int, int) { return 0, 0 }
