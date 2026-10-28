package qoder

import (
	"path/filepath"
	"strings"

	"github.com/muyi99/agent-memory/internal/domain"
)

// metadata.type → 记忆类型。Qoder 只会写 user/feedback/project/reference 四种。
var typeByMetadata = map[string]domain.MemoryType{
	"user":      domain.TypePreference,
	"feedback":  domain.TypeConstraint,
	"project":   domain.TypeFact,
	"reference": domain.TypeFact,
}

// ParseMemoryFile 解析一个 Qoder 记忆文件：整篇正文是一条记忆。
// 返回值第二参数为 false 表示这个文件不产出内容（索引文件、空文件）。
func ParseMemoryFile(path string, content []byte) (domain.MemoryDraft, bool, error) {
	if strings.EqualFold(filepath.Base(path), "MEMORY.md") {
		return domain.MemoryDraft{}, false, nil
	}
	// BOM 会让首行的 "---" 认不出来，frontmatter 整个失效（Windows 编辑器常见）；
	// CRLF 会把 \r 带进正文与哈希，这里统一成 LF。
	raw := strings.TrimPrefix(string(content), "\ufeff")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	text := strings.TrimSpace(raw)
	if text == "" {
		return domain.MemoryDraft{}, false, nil
	}

	typ := domain.TypeFact
	body := text
	if fm, rest, ok := splitFrontMatter(text); ok {
		if mapped, hit := typeByMetadata[fm["type"]]; hit {
			typ = mapped
		}
		body = strings.TrimSpace(rest)
		if body == "" {
			body = strings.TrimSpace(fm["description"])
		}
	}
	if body == "" {
		return domain.MemoryDraft{}, false, nil
	}
	return domain.MemoryDraft{Type: typ, Text: body, SourcePath: path, Quote: body}, true, nil
}

// splitFrontMatter 只取本项目用得上的三个键：name、description、metadata.type。
// 不引入 YAML 依赖：这些 frontmatter 由 agent 自己生成，形状固定，手写解析更好控制失败行为。
func splitFrontMatter(s string) (map[string]string, string, bool) {
	lines := strings.Split(s, "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return nil, s, false
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, s, false
	}

	fm := map[string]string{}
	inMetadata := false
	for _, line := range lines[1:end] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		key, value, ok := cutKey(line)
		if !ok {
			continue
		}
		if !indented {
			inMetadata = key == "metadata"
		}
		switch {
		case key == "name":
			fm["name"] = value
		case key == "description":
			fm["description"] = value
		case inMetadata && key == "type":
			fm["type"] = value
		}
	}
	return fm, strings.Join(lines[end+1:], "\n"), true
}

func cutKey(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	i := strings.Index(trimmed, ":")
	if i <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(trimmed[:i]), unquote(strings.TrimSpace(trimmed[i+1:])), true
}

// unquote 只剥「成对」的外层引号。原来是 Trim(v, `"'`)，会把
// description 末尾本就属于内容的单引号一起剥掉（`'预扣'` → `'预扣`）。
func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
