package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// Store 持有唯一的 *sql.DB。全项目只有这个包读写数据库。
//
// 连接数固定为 1：SQLite 的写是串行的，单连接天然避免了 SQLITE_BUSY，
// 也让常驻服务里的扫描与 MCP 写入共用同一条队列。
//
// 使用约束：连接数为 1，禁止在 rows 未读尽、或事务未结束时再向同一个 Store
// 发查询，否则新查询会无限等待空闲连接（静默挂起，不是 panic）。
// 正确姿势是先把 rows 收成切片并关闭，再逐条查询。
type Store struct {
	db   *sql.DB
	path string
}

// dsnEscaper 转义 SQLite URI 路径中有特殊含义的字符：
// ? 会开启查询串，% 会触发百分号解码，# 在实测中会被截断。
// 空格实测可直接放在 URI 路径里，无需转义；路径分隔符保持原样。
var dsnEscaper = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

// sqliteDSN 把数据库路径包装为带连接级 PRAGMA 的 SQLite URI。
// foreign_keys 与 busy_timeout 是连接级设置，随 DSN 下发才能保证
// 连接池重建连接后仍然生效（否则外键级联删除会静默失效）。
//
// _txlock=immediate 让 BeginTx 直接 BEGIN IMMEDIATE 排队拿写锁。默认的 deferred
// 事务「先读后写」时，如果另一个进程（daemon 与 CLI 各开一个 Store）已经拿到写锁，
// 快照升级会立即返回 SQLITE_BUSY——busy_timeout 只对拿锁等待生效，救不了升级冲突。
func sqliteDSN(path string) string {
	return "file:" + dsnEscaper.Replace(filepath.ToSlash(path)) +
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		// 数据目录里有记忆正文与令牌，只给当前用户。
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	fail := func(err error) (*Store, error) {
		db.Close()
		return nil, err
	}

	// journal_mode 切换失败只返回当前模式而不是 error，必须断言结果。
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		return fail(fmt.Errorf("set journal_mode = WAL: %w", err))
	}
	if !strings.EqualFold(mode, "wal") {
		return fail(fmt.Errorf("journal_mode = %q, want wal", mode))
	}
	if _, err := db.Exec(`PRAGMA synchronous = NORMAL`); err != nil {
		return fail(fmt.Errorf("set synchronous = NORMAL: %w", err))
	}
	// foreign_keys 读的是当前连接的实际状态，同样不报错只返回值。
	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fail(fmt.Errorf("read foreign_keys: %w", err))
	}
	if foreignKeys != 1 {
		return fail(fmt.Errorf("foreign_keys = %d, want 1", foreignKeys))
	}

	if _, err := db.Exec(metaDDL); err != nil {
		return fail(fmt.Errorf("create meta table: %w", err))
	}

	ver, ok, err := metaValue(db, "schema_version")
	if err != nil {
		return fail(err)
	}
	if ok && ver != schemaVersion {
		db.Close()
		if versionNewer(ver) {
			return nil, fmt.Errorf("数据库由更新版本的程序创建（schema_version %q > %s），请用新程序或重建", ver, schemaVersion)
		}
		return nil, fmt.Errorf("旧版本库（schema_version %q < %s），需重建（scan --full 会重建索引，但不做 schema 升级）", ver, schemaVersion)
	}

	// 版本兼容后才建表，避免把新版本已删除的对象重新建出来。
	if _, err := db.Exec(schemaSQL); err != nil {
		return fail(fmt.Errorf("create schema: %w", err))
	}
	if !ok {
		if err := setMeta(db, "schema_version", schemaVersion); err != nil {
			return fail(err)
		}
	}

	st := &Store{db: db, path: path}
	if err := st.BackfillTasks(context.Background()); err != nil {
		return fail(err)
	}

	secureDBFiles(path)
	return st, nil
}

// secureDBFiles 尽力把库文件收紧到 0600。-wal/-shm 可能尚未创建，Windows 上的
// Chmod 也只映射只读位；权限收紧失败不该让服务起不来，因此一律忽略错误。
func secureDBFiles(path string) {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		_ = os.Chmod(p, 0o600)
	}
}

// versionNewer 判断库里的版本是否高于当前程序。版本号按整数比较，
// 非数字版本回退为字符串比较，避免 "10" 被误判为低于 "9"。
func versionNewer(ver string) bool {
	got, gotErr := strconv.Atoi(ver)
	want, wantErr := strconv.Atoi(schemaVersion)
	if gotErr == nil && wantErr == nil {
		return got > want
	}
	return ver > schemaVersion
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
