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

// cleanLayout 是两种文本共用的排版清洗：去代码围栏、循环去 bullet、折叠空白。
// 每轮先 TrimSpace，保证结果幂等。
func cleanLayout(s string) string {
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

// DisplayText 是给用户看的版本：只做 NFC 排版清洗，**不做兼容分解**，
// 所以「：；（），！」等全角标点原样保留；也不转小写，标识符与路径的大小写必须保留。
func DisplayText(s string) string {
	return cleanLayout(norm.NFC.String(s))
}

// CanonicalText 产出用于去重比较与 FTS 的文本：先 NFKC 兼容分解（全角 ASCII 折叠、
// 全角标点折成半角），再走同一套排版清洗并小写。哈希/唯一键/分词的输入完全不变。
func CanonicalText(s string) string {
	return strings.ToLower(cleanLayout(norm.NFKC.String(s)))
}

func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
