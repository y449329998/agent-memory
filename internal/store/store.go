package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// ErrEmptyMemory 拒绝把空白内容写成记忆条目。
var ErrEmptyMemory = errors.New("memory text is empty")

// Store 持有唯一的 *sql.DB。全项目只有这个包读写数据库。
//
// 连接数固定为 1：SQLite 的写是串行的，单连接天然避免了 SQLITE_BUSY，
// 也让常驻服务里的扫描与 MCP 写入共用同一条队列。
type Store struct {
	db   *sql.DB
	path string
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	pragmas := []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA synchronous = NORMAL`,
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	ver, ok, err := metaValue(db, "schema_version")
	if err != nil {
		db.Close()
		return nil, err
	}
	if ok && ver != schemaVersion {
		db.Close()
		return nil, fmt.Errorf("database schema version %q is newer than this binary (%s); rebuild with `agent-memory scan --full`", ver, schemaVersion)
	}
	if !ok {
		if err := setMeta(db, "schema_version", schemaVersion); err != nil {
			db.Close()
			return nil, err
		}
	}

	return &Store{db: db, path: path}, nil
}

// Close 关闭底层连接。
func (s *Store) Close() error {
	return s.db.Close()
}

// Path 返回数据库文件路径。
func (s *Store) Path() string {
	return s.path
}

func metaValue(db *sql.DB, key string) (string, bool, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read meta %q: %w", key, err)
	}
	return v, true, nil
}

func setMeta(db *sql.DB, key, value string) error {
	if _, err := db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	); err != nil {
		return fmt.Errorf("write meta %q: %w", key, err)
	}
	return nil
}
