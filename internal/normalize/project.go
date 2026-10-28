package normalize

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	multiSlashRE = regexp.MustCompile(`/+`)
	schemeRE     = regexp.MustCompile(`(?i)^(?:ssh|https?|git)://`)
	scpRE        = regexp.MustCompile(`^[^/@]+@([^:]+):(.+)$`)
	userinfoRE   = regexp.MustCompile(`^[^/@]+@`)
)

// ProjectKey 生成跨 agent 稳定的项目标识：git remote 优先，其次规范化绝对路径。
// 同一个仓库在两个 agent 里路径不同时，只有 remote 能把它们合并成一个项目。
func ProjectKey(gitRemote, absPath string) string {
	if k := keyFromRemote(gitRemote); k != "" {
		return k
	}
	return keyFromPath(absPath)
}

func keyFromRemote(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = schemeRE.ReplaceAllString(s, "")
	if m := scpRE.FindStringSubmatch(s); m != nil {
		s = m[1] + "/" + m[2]
	} else {
		s = userinfoRE.ReplaceAllString(s, "")
	}
	s = strings.ReplaceAll(filepath.ToSlash(s), "\\", "/")
	s = multiSlashRE.ReplaceAllString(s, "/")
	s = strings.Trim(s, "/")
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, ".git")
	s = strings.Trim(s, "/")
	return s
}

func keyFromPath(absPath string) string {
	s := strings.TrimSpace(absPath)
	if s == "" {
		return ""
	}
	s = filepath.Clean(s)
	s = filepath.ToSlash(s)
	s = multiSlashRE.ReplaceAllString(s, "/")
	s = strings.TrimSuffix(s, "/")
	return strings.ToLower(s)
}

// GitRemoteFromConfig 直接读 <dir>/.git/config 取 origin 的 url，不起子进程。
// 返回值第二参数表示是否存在；缺目录、缺 remote 都不算错误。
func GitRemoteFromConfig(dir string) (remote string, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	lines := strings.Split(string(data), "\n")
	inOrigin := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "["):
			inOrigin = trimmed == `[remote "origin"]`
		case inOrigin && strings.HasPrefix(trimmed, "url"):
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) != 2 {
				continue
			}
			v := strings.Trim(strings.TrimSpace(parts[1]), `"`)
			if v == "" {
				continue
			}
			return v, true, nil
		}
	}
	return "", false, nil
}
