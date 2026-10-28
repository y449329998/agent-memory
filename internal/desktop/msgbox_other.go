//go:build !windows

package desktop

import (
	"fmt"
	"os"
)

// MessageBox 在非 Windows 上没有原生消息框，打到 stderr 即可。
func MessageBox(title, text string) {
	fmt.Fprintf(os.Stderr, "[%s] %s\n", title, text)
}
