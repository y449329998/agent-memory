package trae

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// appDirs 是 Trae 两个发行版本的配置目录名，CN 优先。
var appDirs = []string{".trae-cn", ".trae"}

// DiscoverHome 在 base（通常是用户目录）下找 Trae 配置根。
// 判据用 memory/ 或 user_rules/ 的存在性，避免命中同名的空壳目录。
func DiscoverHome(base string) (string, bool) {
	for _, name := range appDirs {
		dir := filepath.Join(base, name)
		if dirExists(filepath.Join(dir, "memory")) || dirExists(filepath.Join(dir, "user_rules")) {
			return dir, true
		}
	}
	return "", false
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// defaultWorkspaceRoots 给出 workspaceStorage 的默认根：Trae CN 优先，其次 Trae。
// 两个目录都可能登记了同一项目，靠前的根优先（见 workspaceIndex）。
func defaultWorkspaceRoots() []string {
	cfg, err := os.UserConfigDir()
	if err != nil || cfg == "" {
		return nil
	}
	return []string{
		filepath.Join(cfg, "Trae CN", "User", "workspaceStorage"),
		filepath.Join(cfg, "Trae", "User", "workspaceStorage"),
	}
}

// slugForPath 复刻 Trae 的项目目录命名规则：
// C:\Users\Admin\Desktop\go-model → -c-users-admin-desktop-go-model
// （转小写、去冒号、反斜杠与斜杠换 -、前缀 -）。
// 项目名本身可以含连字符，所以这个映射只能正向计算，不能字符反解。
func slugForPath(path string) string {
	if path == "" {
		return ""
	}
	s := strings.ToLower(filepath.Clean(path))
	s = strings.ReplaceAll(s, ":", "")
	s = strings.NewReplacer(`\`, "-", "/", "-").Replace(s)
	return "-" + s
}

// slugKey 把项目目录名归一成索引键：小写，并去掉第二个窗口的 --p2-<hash> 后缀。
func slugKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if i := strings.Index(s, "--p2-"); i >= 0 {
		return s[:i]
	}
	return s
}

// projectLocation 用索引判定项目目录的真实路径与置信度。
// 未命中时只给 low，ProjectPath 留空：让 ingest 建 agent 专有的独立项目，
// 而不是按不可靠的 slug 反解去合并。
func projectLocation(dirName string, index map[string]string) (path, confidence string) {
	if p, ok := index[slugKey(dirName)]; ok {
		return p, "high"
	}
	return "", "low"
}

// workspaceIndex 扫描 workspaceStorage 下所有 workspace.json，建立
// slug → 真实路径 的索引。读不到的条目直接跳过：路径映射失败只影响置信度，
// 不应该挡住记忆导入。
func workspaceIndex(roots []string) map[string]string {
	index := make(map[string]string)
	for _, root := range roots {
		if root == "" {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			data, err := os.ReadFile(filepath.Join(root, name, "workspace.json"))
			if err != nil {
				continue
			}
			folder, ok := workspaceFolder(data)
			if !ok {
				continue
			}
			key := slugForPath(folder)
			if key == "" {
				continue
			}
			if _, exists := index[key]; !exists {
				index[key] = folder
			}
		}
	}
	return index
}

// workspaceFolder 解出 workspace.json 里的 folder 字段。
// Trae 存的是 VS Code 的 file URI（file:///d%3A/company/nuonuo-ai）：
// url.Parse 会还原 %3A，再把 URI 路径落成当前平台的本地路径。
func workspaceFolder(data []byte) (string, bool) {
	var ws struct {
		Folder string `json:"folder"`
	}
	if err := json.Unmarshal(data, &ws); err != nil || ws.Folder == "" {
		return "", false
	}
	u, err := url.Parse(ws.Folder)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	p, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		p = u.Path
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		p = "//" + u.Host + p
	}
	p = filepath.FromSlash(p)
	// Windows 的 file URI 路径形如 /d:/x：去掉前导分隔符，保留盘符。
	if len(p) >= 3 && (p[0] == '/' || p[0] == '\\') && p[2] == ':' {
		p = p[1:]
	}
	return filepath.Clean(p), true
}
