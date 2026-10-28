package surface

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DirName 是项目内的输出目录名，M3 的受管区块也指向它。
	DirName  = ".agent-memory"
	FileName = "HANDOFF.md"
)

// WriteHandoff 把简报正文写进 <projectDir>/.agent-memory/HANDOFF.md。
// 内容相同则完全不碰磁盘：重写会刷新 mtime，白耗一次 fsnotify 事件。
func WriteHandoff(projectDir, body string) (path string, changed bool, err error) {
	dir := filepath.Join(projectDir, DirName)
	path = filepath.Join(dir, FileName)

	if existing, err := os.ReadFile(path); err == nil && string(existing) == body {
		return path, false, nil
	} else if err != nil && !os.IsNotExist(err) {
		return path, false, err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return path, false, fmt.Errorf("create %s: %w", dir, err)
	}

	// 临时文件 + rename：读者不会看到半份简报，崩了也不留残缺正文
	tmp, err := os.CreateTemp(dir, ".handoff-*.tmp")
	if err != nil {
		return path, false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return path, false, err
	}
	if err := tmp.Close(); err != nil {
		return path, false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return path, false, fmt.Errorf("publish handoff file: %w", err)
	}
	return path, true, nil
}

// EnsureGitExclude 把条目加进 .git/info/exclude。
// 选它而不是 .gitignore：只影响本机，不会往用户提交的仓库文件里塞内容。
func EnsureGitExclude(projectDir, entry string) (added bool, err error) {
	gitDir := filepath.Join(projectDir, ".git")
	if st, err := os.Stat(gitDir); err != nil || !st.IsDir() {
		return false, nil
	}
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return false, err
	}
	path := filepath.Join(infoDir, "exclude")

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if containsLine(string(existing), entry) {
		return false, nil
	}
	out := string(existing)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += entry + "\n"
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func containsLine(text, entry string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == entry {
			return true
		}
	}
	return false
}
