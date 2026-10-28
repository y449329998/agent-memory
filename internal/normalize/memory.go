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

// DisplayText 是给用户看的版本：与 CanonicalText 共享 NFKC、去代码围栏、去 bullet、
// 折叠空白这些排版清洗，但**不转小写**。标识符与路径的大小写必须保留。
func DisplayText(s string) string {
	s = norm.NFKC.String(s)
	s = strings.ReplaceAll(s, "```", " ")
	for {
		s = strings.TrimSpace(s)
		next := bulletRE.ReplaceAllString(s, "")
		if next == s {
			break
		}
		s = next
	}
	return spaceRE.ReplaceAllString(strings.TrimSpace(s), " ")
}

// CanonicalText 产出用于去重比较的文本：在 DisplayText 的排版清洗之上再小写。
// 归一化结果同时是 FTS 的索引文本，所以两者必须同源。
func CanonicalText(s string) string {
	return strings.ToLower(DisplayText(s))
}

func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
