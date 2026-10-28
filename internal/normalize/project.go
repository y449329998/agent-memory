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
	// scpRE 的 host 不含方括号，冒号后的部分不能以 / 开头，避免把 C:/a 当成 scp。
	scpRE      = regexp.MustCompile(`^(?:[^/@]+@)?([^/:\[\]]+):([^/].*)$`)
	userinfoRE = regexp.MustCompile(`^[^/@]+@`)
	portRE     = regexp.MustCompile(`:\d+$`)
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
	if schemeRE.MatchString(s) {
		// URL 形式：端口属于 authority，必须删掉，不能落成路径段，
		// 否则 ssh://git@host:2222/a/b 会和 https://host/2222/a/b 撞成同一个 key。
		s = schemeRE.ReplaceAllString(s, "")
		s = userinfoRE.ReplaceAllString(s, "")
		s = stripPort(s)
	} else {
		// scp 形式只在原串没有 scheme 时才按冒号切分，否则会把端口当路径。
		s = userinfoRE.ReplaceAllString(s, "")
		if m := scpRE.FindStringSubmatch(s); m != nil {
			s = m[1] + "/" + m[2]
		}
	}
	s = strings.ReplaceAll(filepath.ToSlash(s), "\\", "/")
	s = multiSlashRE.ReplaceAllString(s, "/")
	s = strings.Trim(s, "/")
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, ".git")
	s = strings.Trim(s, "/")
	return s
}

// stripPort 去掉 authority（第一个 / 之前）里的 :digits 端口。
// IPv6 方括号形式只丢方括号后面的端口，保留 [::1]。
func stripPort(s string) string {
	authority, rest, found := strings.Cut(s, "/")
	if strings.HasPrefix(authority, "[") {
		if end := strings.Index(authority, "]"); end >= 0 {
			authority = authority[:end+1]
		}
	} else {
		authority = portRE.ReplaceAllString(authority, "")
	}
	if !found {
		return authority
	}
	return authority + "/" + rest
}

func keyFromPath(absPath string) string {
	s := strings.TrimSpace(absPath)
	if s == "" {
		return ""
	}
	s = filepath.Clean(s)
	s = filepath.ToSlash(s)
	s = strings.ReplaceAll(s, "\\", "/")
	s = multiSlashRE.ReplaceAllString(s, "/")
	if len(s) > 1 {
		s = strings.TrimSuffix(s, "/")
	}
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
