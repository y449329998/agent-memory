package store

import (
	"path/filepath"
	"testing"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpen_CreatesAllSpecTables(t *testing.T) {
	s := openStore(t)
	want := []string{
		"meta", "project", "project_path", "source", "memory_item", "memory_source",
		"session", "session_event", "task_card", "handoff", "agent_link", "memory_fts",
	}
	got := map[string]bool{}
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type IN ('table','view')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got[n] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Fatalf("table %q missing; found %v", name, keys(got))
		}
	}
}

func TestOpen_WALEnabled(t *testing.T) {
	s := openStore(t)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

func TestOpen_RecordsSchemaVersionAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen should succeed: %v", err)
	}
	defer s2.Close()
	var v string
	if err := s2.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Fatalf("schema_version = %q, want %q", v, schemaVersion)
	}
}

func TestOpen_RejectsNewerSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.db.Exec(`UPDATE meta SET value='99' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	if _, err := Open(path); err == nil {
		t.Fatal("want error when db schema_version is newer than binary")
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
