package normalize

import (
	"strings"
	"testing"
)

func TestCanonicalText_StripsBulletMarker(t *testing.T) {
	for _, b := range []string{"-", "*", "+"} {
		with := CanonicalText(b + " Git 提交信息必须使用中文")
		plain := CanonicalText("Git 提交信息必须使用中文")
		if with != plain {
			t.Fatalf("bullet %q not stripped: %q != %q", b, with, plain)
		}
	}
}

func TestCanonicalText_CollapsesWhitespaceAcrossLines(t *testing.T) {
	a := CanonicalText("不在  foreach\n\t 内做数据查询")
	b := CanonicalText("不在 foreach 内做数据查询")
	if a != b {
		t.Fatalf("a=%q b=%q", a, b)
	}
}

func TestCanonicalText_NFKCMergesFullWidthForms(t *testing.T) {
	if CanonicalText("ＡＢＣ") != CanonicalText("abc") {
		t.Fatal("full-width ASCII should canonicalize equal")
	}
}

func TestCanonicalText_KeepsMinusInsideIdentifier(t *testing.T) {
	got := CanonicalText("nuonuo-fresh3-api 项目")
	if !strings.Contains(got, "nuonuo-fresh3-api") {
		t.Fatalf("identifier damaged: %q", got)
	}
}

func TestHash_StableHex64(t *testing.T) {
	h := Hash("abc")
	if len(h) != 64 || h != Hash("abc") {
		t.Fatalf("bad hash %q", h)
	}
	if h == Hash("abd") {
		t.Fatal("collision on trivial input")
	}
}

func TestHashBytes_MatchesHashOfContent(t *testing.T) {
	if HashBytes([]byte("abc")) != Hash("abc") {
		t.Fatal("HashBytes and Hash must agree")
	}
}

func TestDisplayText_PreservesCaseButCleansLayout(t *testing.T) {
	got := DisplayText("-  matchGoodsSku 与 /Home/User 路径\n")
	want := "matchGoodsSku 与 /Home/User 路径"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if CanonicalText("-  matchGoodsSku") != "matchgoodssku" {
		t.Fatal("CanonicalText must still lowercase for dedupe")
	}
}

func TestCanonicalText_IsIdempotentAcrossUnicodeWhitespace(t *testing.T) {
	inputs := []string{
		"\u2028- x",
		"\u2029* y",
		"\u0085+ z",
		"\u00a0- 全角空格 bullet",
		"- - 连续 bullet",
		"普通文本",
	}
	for _, in := range inputs {
		once := CanonicalText(in)
		twice := CanonicalText(once)
		if once != twice {
			t.Fatalf("not idempotent for %q: first=%q second=%q", in, once, twice)
		}
	}
}
