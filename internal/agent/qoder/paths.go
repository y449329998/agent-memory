package qoder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DiscoverHome 在 base（通常是用户目录）下找 Qoder 配置根。
// 本机同时存在 ~/.qoder 与 ~/.qoder-cn，取真正装着 memory/projects 的那个；
// 判据用子目录存在性，避免命中同名的空壳目录。
func DiscoverHome(base string) (string, bool) {
	for _, name := range []string{".qoder-cn", ".qoder"} {
		dir := filepath.Join(base, name)
		if dirExists(filepath.Join(dir, "memory")) || dirExists(filepath.Join(dir, "projects")) {
			return dir, true
		}
	}
	return "", false
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// ProjectPathFromSessions 反查一个 Qoder projects/<slug> 目录对应的真实工作目录。
// 会话 JSONL 里的 workspace-directories 行是权威来源；找不到时退回 slug 反解并标 low。
func ProjectPathFromSessions(projectDir string) (path string, confidence string) {
	files, err := filepath.Glob(filepath.Join(projectDir, "*.jsonl"))
	if err == nil {
		sort.Strings(files)
		for _, f := range files {
			if p, ok := firstWorkspaceDir(f); ok {
				return p, "high"
			}
		}
	}
	if p, ok := decodeSlug(filepath.Base(projectDir)); ok {
		return p, "low"
	}
	return "", "low"
}

type workspaceLine struct {
	Type        string   `json:"type"`
	Directories []string `json:"directories"`
}

func firstWorkspaceDir(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// 单行可能是几十 KB 的技能正文，甚至几 MB；默认 64KB 上限会直接报 too long。
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	// 扫到命中或 EOF：workspace 行可能被很长的技能正文挤到几千行之后，
	// 截断行数会让记忆与会话各自解析出不同的项目路径。
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte("workspace-directories")) {
			continue
		}
		var w workspaceLine
		if err := json.Unmarshal(line, &w); err != nil {
			continue
		}
		if w.Type != "workspace-directories" || len(w.Directories) == 0 {
			continue
		}
		if w.Directories[0] == "" {
			continue
		}
		return w.Directories[0], true
	}
	return "", false
}

// decodeSlug 把 Qoder 的目录名猜回路径，只用于展示。
// 不可靠是已知的：项目名本身含连字符（nuonuo-fresh3-api），'-' 与层级分隔符无法区分，
// 所以调用方拿到的一律是 low 置信度，ingest 不据此合并项目。
func decodeSlug(slug string) (string, bool) {
	i := strings.Index(slug, "--")
	if i <= 0 {
		return "", false
	}
	drive := strings.ToUpper(slug[:i])
	if len(drive) != 1 {
		return "", false
	}
	rest := strings.ReplaceAll(slug[i+2:], "-", string(filepath.Separator))
	if rest == "" {
		return "", false
	}
	return drive + ":" + string(filepath.Separator) + rest, true
}
