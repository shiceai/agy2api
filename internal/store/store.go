// Package store 封装 SQLite 的打开、迁移与数据访问。
// 热路径（选号、冷却、令牌缓存）不经过本包；这里只存账号、API Key 和用量记录。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"

	"github.com/shiceai/agy2api/ddl"
)

// 账号和 API Key 的状态。
const (
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"
)

// Encryptor 加解密库里的敏感字段（refresh token、client secret），实现见 internal/cryptox。
type Encryptor interface {
	Encrypt(string) (string, error)
	Decrypt(string) (string, error)
}

// Store 持有读写两个连接池。SQLite 同一时刻只允许一个写入者，
// 因此写池限制为单连接；WAL 下读不阻塞写，读池可放多连接。
type Store struct {
	writer *sql.DB
	reader *sql.DB
	// unknown 是库里执行过、但这个程序里没有的迁移：库被更新的版本改过结构。
	unknown []string
}

// Open 打开（必要时创建）数据库，执行 ddl 目录中尚未执行的迁移，返回 Store。
func Open(dataDir string) (*Store, error) {
	return open(dataDir, ddl.FS)
}

func open(dataDir string, migrations fs.FS) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dbPath := filepath.Join(dataDir, "agy2api.db")
	dsn := "file:" + dbPath + "?_txlock=immediate" +
		"&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"

	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)

	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	reader.SetMaxOpenConns(8)

	s := &Store{writer: writer, reader: reader}
	if err := s.migrate(migrations); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭两个连接池。
func (s *Store) Close() error {
	var first error
	if err := s.reader.Close(); err != nil {
		first = err
	}
	if err := s.writer.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

// migrate 按文件名顺序执行尚未执行过的迁移。每个文件一个事务，
// 执行记录写入 schema_migrations，保证加列等不可重复执行的语句只跑一次。
func (s *Store) migrate(migrations fs.FS) error {
	if _, err := s.writer.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT    PRIMARY KEY,
		applied_at INTEGER NOT NULL
	) STRICT`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := s.appliedMigrations()
	if err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}
	for name := range applied {
		if !known[name] {
			s.unknown = append(s.unknown, name)
		}
	}
	sort.Strings(s.unknown)
	for _, name := range names {
		if applied[name] {
			continue
		}
		query, err := fs.ReadFile(migrations, name)
		if err != nil {
			return err
		}
		if err := s.applyMigration(name, string(query)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

// UnknownMigrations 返回库里执行过、但这个程序里没有的迁移（文件名）。不为空说明库被更新的版本改过结构，
// 当前程序可能不兼容（换成了更旧的程序）。
func (s *Store) UnknownMigrations() []string { return s.unknown }

// MigrationsSince 返回 since（Unix 毫秒）之后执行的迁移数。撤销在线升级前用它判断新版本有没有改过库的结构。
func (s *Store) MigrationsSince(ctx context.Context, since int64) (int, error) {
	var n int
	err := s.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE applied_at >= ?`, since).Scan(&n)
	return n, err
}

// BackupTo 把整个库导出到 dst（VACUUM INTO，得到一份一致、紧凑的副本），dst 不能已经存在。
// 走读连接，不挡写入。
func (s *Store) BackupTo(ctx context.Context, dst string) error {
	_, err := s.reader.ExecContext(ctx, `VACUUM INTO ?`, dst)
	return err
}

func (s *Store) appliedMigrations() (map[string]bool, error) {
	rows, err := s.writer.Query(`SELECT name FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		applied[name] = true
	}
	return applied, rows.Err()
}

func (s *Store) applyMigration(name, query string) error {
	tx, err := s.writer.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(query); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
		name, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// execCtx 是写操作的便捷封装。
func (s *Store) execCtx(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.writer.ExecContext(ctx, query, args...)
}
