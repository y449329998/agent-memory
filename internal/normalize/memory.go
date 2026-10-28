package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var (
	bulletRE = regexp.MustCompile(`^\s*[-*+]\s+`)
	spaceRE  = regexp.MustCompile(`\s+`)
)

// CanonicalText 产出用于去重比较的文本：NFKC → 去代码围栏 → 反复去 bullet 前缀
// → 折叠空白 → 小写。归一化结果同时是 FTS 的索引文本，所以两者必须同源。
func CanonicalText(s string) string {
	s = norm.NFKC.String(s)
	s = strings.ReplaceAll(s, "```", " ")
	for {
		next := bulletRE.ReplaceAllString(s, "")
		if next == s {
			break
		}
		s = next
	}
	s = spaceRE.ReplaceAllString(strings.TrimSpace(s), " ")
	return strings.ToLower(s)
}

func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
