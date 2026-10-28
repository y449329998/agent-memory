package surface

import (
	"os"
	"path/filepath"
	"strings"
)

// protectedAgentDirs 是绝不能写入的 agent 原生配置目录（绝对路径）。
// 覆盖各家 agent 在用户目录下的默认布局。
func protectedAgentDirs(home string) []string {
	return []string{
		filepath.Join(home, ".qoder-cn"),
		filepath.Join(home, ".qoder"),
		filepath.Join(home, ".trae-cn"),
		filepath.Join(home, ".trae"),
		filepath.Join(home, ".workbuddy"),
		filepath.Join(home, ".config", "opencode"),
		filepath.Join(home, ".claude"),
	}
}

// IsProtectedAgentDir 判断路径是否落在任何 agent 的原生配置目录里。
// 命中意味着调用方可能把简报写进 agent 自己的配置树，必须拒绝。
// CLI 的 handoff/scan 与 daemon 的写接口共用这一份判断。
func IsProtectedAgentDir(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || path == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	for _, dir := range protectedAgentDirs(home) {
		rel, err := filepath.Rel(filepath.Clean(dir), abs)
		if err != nil || filepath.IsAbs(rel) {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}
