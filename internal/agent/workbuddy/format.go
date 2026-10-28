package workbuddy

import (
	"strings"

	"github.com/muyi99/agent-memory/internal/domain"
)

// sectionRules 按判定顺序把分节标题映射到记忆类型：先匹配到的规则生效。
// 「风格」同时出现在 preference 与 convention，按顺序归 preference。
var sectionRules = []struct {
	keywords []string
	typ      domain.MemoryType
}{
	{[]string{"偏好", "preference", "style", "风格"}, domain.TypePreference},
	{[]string{"风格", "规范", "约定", "convention", "流程", "workflow"}, domain.TypeConvention},
	{[]string{"约束", "constraint", "禁止"}, domain.TypeConstraint},
	{[]string{"教训", "lesson", "经验"}, domain.TypeLesson},
}

// ParseMemoryFile 解析 WorkBuddy 的 MEMORY.md：
// 一条顶层 bullet 一条记忆，缩进的子 bullet 与续行都并入父条目并保留换行。
// 返回空切片表示文件没有可导入内容（空文件、没有 bullet）。
func ParseMemoryFile(path string, content []byte) ([]domain.MemoryDraft, error) {
	body := stripFrontMatter(normalizeText(content))
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}

	var drafts []domain.MemoryDraft
	typ := domain.TypeFact
	current := -1 // 当前顶层 bullet 在 drafts 中的下标；-1 表示没有可并入的父条目
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			current = -1
			continue
		}
		if isIndented(line) {
			// 只有缩进行属于上一条顶层 bullet；子 bullet 去掉列表标记。
			if current < 0 {
				continue
			}
			item, ok := bulletText(trimmed)
			if !ok {
				item = trimmed
			}
			if item == "" {
				continue
			}
			drafts[current].Text += "\n" + item
			drafts[current].Quote = drafts[current].Text
			continue
		}
		if title, ok := headingText(trimmed); ok {
			typ = typeForSection(title)
			current = -1
			continue
		}
		item, ok := bulletText(trimmed)
		if !ok {
			// 顶层普通正文不是记忆，也不接收后续续行。
			current = -1
			continue
		}
		if item == "" { // 空 bullet 不产出记忆，也不接收后续续行
			current = -1
			continue
		}
		drafts = append(drafts, domain.MemoryDraft{
			Type: typ, Text: item, SourcePath: path, Quote: item,
		})
		current = len(drafts) - 1
	}
	return drafts, nil
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

// typeForSection 按关键词（大小写不敏感）匹配标题；未知标题归 fact。
func typeForSection(title string) domain.MemoryType {
	lower := strings.ToLower(title)
	for _, rule := range sectionRules {
		for _, kw := range rule.keywords {
			if strings.Contains(lower, kw) {
				return rule.typ
			}
		}
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
