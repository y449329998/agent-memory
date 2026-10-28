package normalize

import (
	"sort"
	"strings"
	"testing"
)

func TestSegment_IndexesSingleCharsAndBigrams(t *testing.T) {
	got := Segment("用户偏好 snake_case 命名")
	want := []string{"用", "用户", "户", "户偏", "偏", "偏好", "好", "snake_case", "命", "命名", "名"}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestSegment_SplitsAtPunctuation(t *testing.T) {
	got := Segment("查询构造器，闭包外用 $list")
	// 索引侧同时输出单字与二元组，二者在切片里交替出现（查、查询、询、询构…），
	// 所以只能按 token 判断，不能断言二元组在拼接串里连续出现。
	tokens := make(map[string]bool, len(got))
	for _, tok := range got {
		tokens[tok] = true
	}
	for _, want := range []string{"查询", "询构", "构造"} {
		if !tokens[want] {
			t.Fatalf("missing bigram %q in %v", want, got)
		}
	}
	// 标点必须切断汉字串：不能出现跨「，」或「$」的二元组
	for _, bad := range []string{"器闭", "用l"} {
		if tokens[bad] {
			t.Fatalf("punctuation did not split the run: found %q in %v", bad, got)
		}
	}
	if !tokens["list"] {
		t.Fatalf("expected ascii token list, got %v", got)
	}
}

func TestSegment_KeepsHyphenatedAndUnderscoreIdentifiers(t *testing.T) {
	got := strings.Join(Segment("nuonuo-fresh3-api similar_words"), "|")
	for _, tok := range []string{"nuonuo-fresh3-api", "similar_words"} {
		if !strings.Contains(got, tok) {
			t.Fatalf("token %q missing in %q", tok, got)
		}
	}
}

func TestQueryTokens_DropsSingleCharsForAdjacentPhrase(t *testing.T) {
	got := QueryTokens("中文偏好")
	join := strings.Join(got, "|")
	if join != "中文|文偏|偏好" {
		t.Fatalf("got %q, want %q", join, "中文|文偏|偏好")
	}
}

func TestQueryTokens_FallsBackToSingleChar(t *testing.T) {
	got := QueryTokens("币")
	if len(got) != 1 || got[0] != "币" {
		t.Fatalf("got %v, want [币]", got)
	}
}

func TestQueryTokens_AsciiOnlyQuery(t *testing.T) {
	got := QueryTokens("snake_case")
	if len(got) != 1 || got[0] != "snake_case" {
		t.Fatalf("got %v", got)
	}
}

func TestMatchExpr_QuotesEveryToken(t *testing.T) {
	got := MatchExpr(`a"b`)
	if got == "" {
		t.Fatal("empty expr")
	}
	if strings.Count(got, `"`)%2 != 0 {
		t.Fatalf("unbalanced quotes: %q", got)
	}
}
