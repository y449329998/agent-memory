package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
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

// TestOpen_SecuresDataDirAndDBFiles 锁定数据目录与库文件的权限：
// 目录 0700、库文件（含 -wal/-shm）0600。Windows 只映射只读位，POSIX 权限
// 无法断言，因此那里只验证文件存在且 Chmod 的失败没有让 Open 报错。
func TestOpen_SecuresDataDirAndDBFiles(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dataDir, "memory.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	for _, p := range []string{dataDir, path} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s must exist after Open: %v", p, err)
		}
	}
	if runtime.GOOS == "windows" {
		// Windows 的文件模式不区分 0755/0700，只认只读位；能走到这里说明
		// Open 没有被 Chmod 失败打断。
		return
	}

	if info, err := os.Stat(dataDir); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("data dir perm = %o, want 700", perm)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if err != nil {
			// WAL/SHM 可能尚未创建或已合并；存在才检查。
			continue
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s perm = %o, want 600", p, perm)
		}
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

// TestOpen_ChecksVersionBeforeSchemaDDL 回归：版本不兼容时 Open 不得执行建表语句。
// 旧实现先建表再校验版本，会在高版本库上把新版已删除的表重新建出来。
func TestOpen_ChecksVersionBeforeSchemaDDL(t *testing.T) {
	cases := []struct {
		name     string
		version  string
		wantText string
	}{
		{"newer", "99", "更新版本"},
		{"older", "0", "旧版本"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memory.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE future_only (id INTEGER PRIMARY KEY)`); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO meta(key, value) VALUES('schema_version', ?)`, tc.version); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			if _, err := Open(path); err == nil {
				t.Fatal("Open succeeded on an incompatible schema version")
			} else {
				if !strings.Contains(err.Error(), tc.wantText) {
					t.Fatalf("error %q does not mention %q", err, tc.wantText)
				}
				if !strings.Contains(err.Error(), tc.version) || !strings.Contains(err.Error(), schemaVersion) {
					t.Fatalf("error %q should carry both versions %q and %q", err, tc.version, schemaVersion)
				}
			}

			db, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
			if err != nil {
				t.Fatal(err)
			}
			var tables []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				tables = append(tables, name)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			rows.Close()

			want := []string{"future_only", "meta"}
			if !reflect.DeepEqual(tables, want) {
				t.Fatalf("Open mutated the schema before the version check: tables = %v, want %v", tables, want)
			}
		})
	}
}

// TestSchema_ColumnsAndUniques 断言关键列与唯一索引的形状，防止 DDL 被改坏。
func TestSchema_ColumnsAndUniques(t *testing.T) {
	s := openStore(t)

	cols := tableColumns(t, s.db, "memory_item")
	wantCols := []string{
		"scope", "project_id", "type", "text", "text_norm_hash", "stale",
		"first_seen_at", "last_seen_at", "embedding", "embed_model", "embed_dim", "embedded_at",
	}
	for _, name := range wantCols {
		if _, ok := cols[name]; !ok {
			t.Errorf("memory_item missing column %q (have %v)", name, columnNames(cols))
		}
	}
	pid, ok := cols["project_id"]
	if !ok {
		t.Fatal("memory_item.project_id missing")
	}
	if !pid.notNull {
		t.Error("memory_item.project_id must be NOT NULL")
	}
	if !pid.dflt.Valid || pid.dflt.String != "0" {
		t.Errorf("memory_item.project_id default = %v, want 0", pid.dflt)
	}

	uniques := []struct {
		table string
		cols  []string
	}{
		{"memory_item", []string{"scope", "project_id", "type", "text_norm_hash"}},
		{"source", []string{"agent", "kind", "path"}},
		{"memory_source", []string{"memory_id", "agent", "path"}},
		{"session", []string{"agent", "external_id"}},
		{"task_card", []string{"session_id"}},
	}
	for _, u := range uniques {
		u := u
		t.Run(u.table, func(t *testing.T) {
			got := uniqueIndexColumns(t, s.db, u.table)
			if len(got) != 1 {
				t.Fatalf("%s has %d unique indexes (%v), want exactly one with columns %v", u.table, len(got), got, u.cols)
			}
			if !reflect.DeepEqual(got[0], u.cols) {
				t.Fatalf("%s unique index columns = %v, want %v", u.table, got[0], u.cols)
			}
		})
	}

	ftsCols := tableColumns(t, s.db, "memory_fts")
	if len(ftsCols) != 1 {
		t.Fatalf("memory_fts columns = %v, want only text_seg", columnNames(ftsCols))
	}
	if _, ok := ftsCols["text_seg"]; !ok {
		t.Fatalf("memory_fts columns = %v, want text_seg", columnNames(ftsCols))
	}
}

// TestOpen_PragmasSurviveReconnect 断言连接级 PRAGMA 来自 DSN：
// 连接被池丢弃后新建连接仍须带 foreign_keys 与 busy_timeout。
func TestOpen_PragmasSurviveReconnect(t *testing.T) {
	s := openStore(t)
	// 不保留空闲连接，使每次查询都用一条新建连接。
	s.db.SetMaxIdleConns(0)
	for i := 0; i < 3; i++ {
		var fk int
		if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if fk != 1 {
			t.Fatalf("iteration %d: foreign_keys = %d, want 1", i, fk)
		}
		var busy int
		if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if busy != 5000 {
			t.Fatalf("iteration %d: busy_timeout = %d, want 5000", i, busy)
		}
	}
}

// TestOpen_PathWithURICharacters 覆盖 DSN 化后 Windows 上常见的空格路径，
// 以及 URI 中有特殊含义的 #、%。
func TestOpen_PathWithURICharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data dir #1 100%")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "memory.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	defer s.Close()
	if s.Path() != path {
		t.Fatalf("Store.Path() = %q, want %q", s.Path(), path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database was not created at %q: %v", path, err)
	}
}

type columnInfo struct {
	notNull bool
	dflt    sql.NullString
}

func tableColumns(t *testing.T, db *sql.DB, table string) map[string]columnInfo {
	t.Helper()
	rows, err := db.Query(`SELECT name, "notnull", dflt_value FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	cols := map[string]columnInfo{}
	for rows.Next() {
		var name string
		var notNull int
		var dflt sql.NullString
		if err := rows.Scan(&name, &notNull, &dflt); err != nil {
			t.Fatalf("scan pragma_table_info(%s): %v", table, err)
		}
		cols[name] = columnInfo{notNull: notNull == 1, dflt: dflt}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pragma_table_info(%s): %v", table, err)
	}
	return cols
}

// uniqueIndexColumns 返回每一条唯一索引的列（按 seqno 排序）。
// 先收齐索引名并关闭 rows，避免单连接下再发查询造成挂起。
func uniqueIndexColumns(t *testing.T, db *sql.DB, table string) [][]string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_index_list(?) WHERE "unique" = 1 ORDER BY name`, table)
	if err != nil {
		t.Fatalf("pragma_index_list(%s): %v", table, err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()

	out := make([][]string, 0, len(names))
	for _, name := range names {
		out = append(out, indexColumns(t, db, name))
	}
	return out
}

func indexColumns(t *testing.T, db *sql.DB, index string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index)
	if err != nil {
		t.Fatalf("pragma_index_info(%s): %v", index, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan pragma_index_info(%s): %v", index, err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pragma_index_info(%s): %v", index, err)
	}
	return cols
}

func columnNames(cols map[string]columnInfo) []string {
	out := make([]string, 0, len(cols))
	for name := range cols {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
