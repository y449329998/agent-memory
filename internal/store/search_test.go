package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func seedForSearch(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	items := []struct {
		text string
		typ  domain.MemoryType
	}{
		{"查询构造器变量不要命名为 $query，闭包外用 $list", domain.TypeConvention},
		{"Git commit messages 必须使用中文", domain.TypePreference},
		{"nuonuo-fresh3-api 的 matchGoodsSku 接口限制 similar_words 最多 10 个", domain.TypeConstraint},
		{"用户偏好 snake_case 命名", domain.TypePreference},
	}
	for i, it := range items {
		d := domain.MemoryDraft{
			Scope:       domain.ScopeUser,
			Type:        it.typ,
			Text:        it.text,
			SourceAgent: "qoder",
			SourcePath:  fmt.Sprintf("/q/item%d.md", i),
		}
		if _, _, err := s.UpsertMemory(ctx, 0, d, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
}

func search(t *testing.T, s *Store, q string) []string {
	t.Helper()
	hits, err := (FTS{store: s}).Search(context.Background(), domain.SearchQuery{Text: q, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Memory.Text)
	}
	return out
}

func TestFTS_SearchFindsTwoCharChineseTerm(t *testing.T) {
	s := openStore(t)
	seedForSearch(t, s)
	got := search(t, s, "偏好")
	if len(got) == 0 || !strings.Contains(got[0], "用户偏好") {
		t.Fatalf("两字中文词应命中, got %v", got)
	}
}

func TestFTS_SearchFindsIdentifierTerm(t *testing.T) {
	s := openStore(t)
	seedForSearch(t, s)
	got := search(t, s, "similar_words")
	// 入库文本保留原文大小写，检索结果应原样带出标识符。
	if len(got) != 1 || !strings.Contains(got[0], "matchGoodsSku") {
		t.Fatalf("want the api constraint, got %v", got)
	}
}

func TestFTS_PhraseQueryNarrowsResults(t *testing.T) {
	s := openStore(t)
	seedForSearch(t, s)
	got := search(t, s, "查询构造器")
	if len(got) != 1 || !strings.Contains(got[0], "$list") {
		t.Fatalf("want only the convention item, got %v", got)
	}
}

func TestFTS_TypeFilterAndStaleExclusion(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	seedForSearch(t, s)

	hits, err := (FTS{store: s}).Search(ctx, domain.SearchQuery{Text: "命名", Type: domain.TypePreference, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Memory.Type != domain.TypePreference {
			t.Fatalf("type filter leaked %q", h.Memory.Text)
		}
	}

	// 把偏好类条目全部标 stale，默认检索应看不到
	if _, err := s.db.Exec(`UPDATE memory_item SET stale = 1 WHERE type = 'preference'`); err != nil {
		t.Fatal(err)
	}
	hidden, err := (FTS{store: s}).Search(ctx, domain.SearchQuery{Text: "命名", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hidden {
		if h.Memory.Type == domain.TypePreference {
			t.Fatalf("stale item leaked into default search: %q", h.Memory.Text)
		}
	}
	withStale, err := (FTS{store: s}).Search(ctx, domain.SearchQuery{Text: "命名", IncludeStale: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(withStale) <= len(hidden) {
		t.Fatalf("include_stale should return more: %d vs %d", len(withStale), len(hidden))
	}
}

func TestFTS_EmptyAndPunctuationOnlyQueriesReturnNoError(t *testing.T) {
	s := openStore(t)
	seedForSearch(t, s)
	for _, q := range []string{"", "   ", "，。", `"}:`} {
		hits, err := (FTS{store: s}).Search(context.Background(), domain.SearchQuery{Text: q, Limit: 5})
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		if len(hits) != 0 {
			t.Fatalf("query %q should match nothing, got %v", q, hits)
		}
	}
}

func TestFTS_ResultCarriesSourcesAndOrigin(t *testing.T) {
	s := openStore(t)
	seedForSearch(t, s)
	hits, err := (FTS{store: s}).Search(context.Background(), domain.SearchQuery{Text: "中文", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	h := hits[0]
	if h.Origin != "fts" {
		t.Fatalf("origin = %q", h.Origin)
	}
	if len(h.Memory.Sources) == 0 || h.Memory.Sources[0].Agent != "qoder" {
		t.Fatalf("sources missing: %+v", h.Memory.Sources)
	}
}

func TestFTS_ProjectSearchAlsoReturnsUserScope(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	global := domain.MemoryDraft{Scope: domain.ScopeUser, Type: domain.TypePreference, Text: "沟通用中文", SourceAgent: "qoder", SourcePath: "/u.md"}
	if _, _, err := s.UpsertMemory(ctx, 0, global, now); err != nil {
		t.Fatal(err)
	}
	local := domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "本项目的中文文档放在 docs 下", SourceAgent: "trae", SourcePath: "/p.md"}
	if _, _, err := s.UpsertMemory(ctx, 3, local, now); err != nil {
		t.Fatal(err)
	}
	other := domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "别的项目的中文说明", SourceAgent: "trae", SourcePath: "/o.md"}
	if _, _, err := s.UpsertMemory(ctx, 9, other, now); err != nil {
		t.Fatal(err)
	}

	hits, err := (FTS{store: s}).Search(ctx, domain.SearchQuery{Text: "中文", ProjectID: 3, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var sawGlobal, sawLocal, sawOther bool
	for _, h := range hits {
		switch {
		case strings.Contains(h.Memory.Text, "沟通用中文"):
			sawGlobal = true
		case strings.Contains(h.Memory.Text, "docs"):
			sawLocal = true
		default:
			sawOther = true
		}
	}
	if !sawGlobal || !sawLocal {
		t.Fatalf("want global+local, got global=%v local=%v", sawGlobal, sawLocal)
	}
	if sawOther {
		t.Fatal("other project's item leaked")
	}
}
