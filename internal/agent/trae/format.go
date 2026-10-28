package trae

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/muyi99/agent-memory/internal/domain"
)

// 分节标题 → 记忆类型。键是「去空白 + 小写」后的标题，匹配时对大小写和空白不敏感。
var typeBySection = map[string]domain.MemoryType{
	"hardconstraints":        domain.TypeConstraint,
	"constraints":            domain.TypeConstraint,
	"约束":                     domain.TypeConstraint,
	"engineeringconventions": domain.TypeConvention,
	"conventions":            domain.TypeConvention,
	"规范":                     domain.TypeConvention,
	"约定":                     domain.TypeConvention,
	"lessonslearned":         domain.TypeLesson,
	"lessons":                domain.TypeLesson,
	"教训":                     domain.TypeLesson,
	"经验":                     domain.TypeLesson,
	"userpreferences":        domain.TypePreference,
	"preferences":            domain.TypePreference,
	"偏好":                     domain.TypePreference,
	"techstack":              domain.TypeFact,
	"技术栈":                    domain.TypeFact,
}

// ParseMemoryFile 解析 user_profile.md / project_memory.md：
// 一条 bullet 一条记忆，缩进的续行并入上一条。返回空切片表示这个文件
// 没有可导入内容（索引文件、空文件、没有 bullet）。
//
// 解析本身不会失败；error 返回值与适配器的错误单元协议保持一致，
// 将来加结构校验时不需要改调用方。
func ParseMemoryFile(path string, content []byte) ([]domain.MemoryDraft, error) {
	if strings.EqualFold(filepath.Base(path), "MEMORY.md") {
		return nil, nil
	}
	body := stripFrontMatter(normalizeText(content))
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}

	var drafts []domain.MemoryDraft
	typ := domain.TypeFact
	current := -1 // 当前 bullet 在 drafts 中的下标；-1 表示没有可续写的 bullet
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			current = -1
			continue
		}
		if title, ok := headingText(trimmed); ok {
			typ = typeForSection(title)
			current = -1
			continue
		}
		if item, ok := bulletText(trimmed); ok {
			if item == "" { // 空 bullet 不产出记忆，也不接收后续续行
				current = -1
				continue
			}
			drafts = append(drafts, domain.MemoryDraft{
				Type: typ, Text: item, SourcePath: path, Quote: item,
			})
			current = len(drafts) - 1
			continue
		}
		// 只有缩进的续行属于上一条 bullet；顶层普通正文不是记忆。
		if current >= 0 && isIndented(line) {
			drafts[current].Text += "\n" + trimmed
			drafts[current].Quote = drafts[current].Text
			continue
		}
		current = -1
	}
	return drafts, nil
}

// ParseRuleFile 把 user_rules/*.md 整篇当作一条 preference：
// 规则是纯文本，不分节、不按 bullet 拆，多行原样保留（只做 BOM/CRLF 归一与首尾 trim）。
func ParseRuleFile(path string, content []byte) ([]domain.MemoryDraft, error) {
	text := strings.TrimSpace(normalizeText(content))
	if text == "" {
		return nil, nil
	}
	return []domain.MemoryDraft{{
		Type: domain.TypePreference, Text: text, SourcePath: path, Quote: text,
	}}, nil
}

// normalizeText 去掉 Windows 编辑器常见的 BOM，并把 CRLF 统一成 LF，
// 避免 \r 进入正文与内容哈希。
func normalizeText(content []byte) string {
	raw := strings.TrimPrefix(string(content), "\ufeff")
	return strings.ReplaceAll(raw, "\r\n", "\n")
}

// stripFrontMatter 丢弃开头的 YAML frontmatter；没有成对的 --- 时按正文处理。
func stripFrontMatter(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return s
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return s
}

// headingText 识别 ATX 标题（1–6 个 #，后跟空白或行尾），返回标题正文。
func headingText(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	i := 0
	for i < len(trimmed) && trimmed[i] == '#' {
		i++
	}
	if i > 6 {
		return "", false
	}
	if i < len(trimmed) && trimmed[i] != ' ' && trimmed[i] != '\t' {
		return "", false
	}
	return strings.TrimSpace(trimmed[i:]), true
}

// typeForSection 去掉标题里的空白、大小写差异和尾部冒号后查表，未知标题归 fact。
func typeForSection(title string) domain.MemoryType {
	key := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, strings.TrimRight(strings.TrimSpace(title), ":："))
	if t, ok := typeBySection[key]; ok {
		return t
	}
	return domain.TypeFact
}

// bulletText 识别无序列表项（-、*、+ 后必须跟空白或行尾）。
func bulletText(trimmed string) (string, bool) {
	if trimmed == "" {
		return "", false
	}
	switch trimmed[0] {
	case '-', '*', '+':
		if len(trimmed) == 1 {
			return "", true
		}
		if trimmed[1] == ' ' || trimmed[1] == '\t' {
			return strings.TrimSpace(trimmed[2:]), true
		}
	}
	return "", false
}

func isIndented(line string) bool {
	return line != "" && (line[0] == ' ' || line[0] == '\t')
}
