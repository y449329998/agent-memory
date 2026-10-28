package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func openForLinks(t *testing.T) (*Store, int64) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	pid, err := st.EnsureProject(ctx, "f:/go/demo", "demo", "", `F:\go\demo`, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	return st, pid
}

func TestRecordAgentLink_ReinstallUpdatesInsteadOfDuplicating(t *testing.T) {
	st, pid := openForLinks(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	if err := st.RecordAgentLink(ctx, pid, "qoder", `F:\go\demo\AGENTS.md`, "hash-a", now); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAgentLink(ctx, pid, "qoder", `F:\go\demo\AGENTS.md`, "hash-b", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := st.QueryInt(ctx, `SELECT count(*) FROM agent_link`)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reinstall must update the row, got %d rows", n)
	}

	links, err := st.ListAgentLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].InstalledHash != "hash-b" {
		t.Fatalf("links = %+v", links)
	}
	if links[0].ProjectID != pid || links[0].Agent != "qoder" {
		t.Fatalf("link identity wrong: %+v", links[0])
	}
}

func TestClearAgentLink_OnUnlink(t *testing.T) {
	st, pid := openForLinks(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	if err := st.RecordAgentLink(ctx, pid, "opencode", `F:\go\demo\AGENTS.md`, "h", now); err != nil {
		t.Fatal(err)
	}
	removed, err := st.ClearAgentLink(ctx, pid, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("want removed=true")
	}
	again, err := st.ClearAgentLink(ctx, pid, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Fatal("second clear is a no-op")
	}
}

func TestOutdatedEmbeddings_SelectsOnlyMissingOrMismatched(t *testing.T) {
	st, pid := openForLinks(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	// 三条记忆：无 embedding、旧模型、当前模型且新鲜
	texts := []string{"甲", "乙", "丙"}
	models := []string{"", "other-model", "text-embed-3-small"}
	embedded := []int64{0, now.Add(-2 * time.Hour).Unix(), now.Unix()}
	ids := make([]int64, 0, 3)
	for i, text := range texts {
		id, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{
			Scope: domain.ScopeProject, Type: domain.TypeFact, Text: text,
			SourceAgent: "test", SourcePath: "/t.md",
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.db.ExecContext(ctx,
			`UPDATE memory_item SET embed_model = ?, embedded_at = ? WHERE id = ?`, models[i], embedded[i], id)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// 「丙」在嵌入之后又被同文本重新见过：embedded_at 早于 last_seen_at 也算过期
	if _, err := st.db.ExecContext(ctx, `UPDATE memory_item SET last_seen_at = ? WHERE id = ?`, now.Add(time.Hour).Unix(), ids[2]); err != nil {
		t.Fatal(err)
	}

	outdated, err := st.OutdatedEmbeddings(ctx, "text-embed-3-small")
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, id := range outdated {
		got[id] = true
	}
	// 甲（没嵌入）、乙（模型不符）、丙（嵌入后又见过）都该在；这里三条全过期，
	// 所以再用一条完全合格的对照：
	if !got[ids[0]] || !got[ids[1]] || !got[ids[2]] {
		t.Fatalf("outdated set = %v want all of %v", outdated, ids)
	}

	// 完全合格的对照必须连 embedding 一起写：只改 embed_model 而向量仍为 NULL 的条目
	// 按定义仍然需要重算。
	if _, err := st.db.ExecContext(ctx,
		`UPDATE memory_item SET embedding=x'00', embed_model='text-embed-3-small', embedded_at=?, last_seen_at=? WHERE id = ?`,
		now.Add(2*time.Hour).Unix(), now.Unix(), ids[2]); err != nil {
		t.Fatal(err)
	}
	outdated2, err := st.OutdatedEmbeddings(ctx, "text-embed-3-small")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range outdated2 {
		if id == ids[2] {
			t.Fatalf("freshly embedded item still counted outdated")
		}
	}

	// 没配模型时只挑完全没嵌入过的：丙 已有向量，不能被列进来。
	outdatedNoModel, err := st.OutdatedEmbeddings(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	noModel := map[int64]bool{}
	for _, id := range outdatedNoModel {
		noModel[id] = true
	}
	if !noModel[ids[0]] {
		t.Fatalf("empty model should still select the never-embedded item: %v", outdatedNoModel)
	}
	if noModel[ids[2]] {
		t.Fatalf("empty model must not select an embedded item: %v", outdatedNoModel)
	}
}
