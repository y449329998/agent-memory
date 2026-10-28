package normalize

import (
	"strings"
	"unicode"
)

// isHan 用 Unicode 脚本属性判断汉字。汉字记忆是本项目的主要语料。
func isHan(r rune) bool {
	return unicode.Is(unicode.Han, r)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

// Segment 产出索引用 token：连续汉字串切成「单字 + 相邻二元组」，
// 字母数字词（含 _ 与 -）原样小写保留。切分边界由任何非标点字母的字符决定。
func Segment(s string) []string {
	var out []string
	var word, han []rune

	flushWord := func() {
		if len(word) > 0 {
			out = append(out, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	flushHan := func() {
		for i := 0; i < len(han); i++ {
			out = append(out, string(han[i]))
			if i+1 < len(han) {
				out = append(out, string(han[i:i+2]))
			}
		}
		han = han[:0]
	}

	for _, r := range s {
		switch {
		case isHan(r):
			flushWord()
			han = append(han, r)
		case isWordRune(r):
			flushHan()
			word = append(word, r)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return out
}

// QueryTokens 是查询侧切分：只保留二元组与整词，丢掉单字。
// 单字进查询会让「中文偏好」这类短语要求语料里存在独立成词的单字，反而降低召回。
// 若二元组与整词都切不出来（单字查询），退回 Segment 结果。
func QueryTokens(s string) []string {
	var out []string
	var word, han []rune

	flushWord := func() {
		if len(word) > 0 {
			out = append(out, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	flushHan := func() {
		for i := 0; i+1 < len(han); i++ {
			out = append(out, string(han[i:i+2]))
		}
		han = han[:0]
	}

	for _, r := range s {
		switch {
		case isHan(r):
			flushWord()
			han = append(han, r)
		case isWordRune(r):
			flushHan()
			word = append(word, r)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()

	if len(out) == 0 {
		return Segment(s)
	}
	return out
}

// MatchExpr 把 token 拼成 FTS5 匹配表达式。每个 token 加双引号并把内部
// 双引号翻倍，避免用户记忆里的符号被当成 FTS 语法。
func MatchExpr(tokens ...string) string {
	parts := make([]string, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	return strings.Join(parts, " ")
}
