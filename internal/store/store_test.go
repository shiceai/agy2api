package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func sqlFile(query string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(query)}
}

func mustOpen(t *testing.T, dir string, migrations fstest.MapFS) *Store {
	t.Helper()
	s, err := open(dir, migrations)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.reader.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	return countRows(t, s, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name) == 1
}

// 加列迁移不可重复执行：每个文件只能跑一次，重启时跳过已执行的文件。
func TestMigrateAppliesEachFileOnce(t *testing.T) {
	dir := t.TempDir()
	v1 := fstest.MapFS{
		"20260101_001_create_items.sql": sqlFile(`CREATE TABLE items (id INTEGER PRIMARY KEY) STRICT;`),
	}
	mustOpen(t, dir, v1).Close()

	v2 := fstest.MapFS{
		"20260101_001_create_items.sql":  v1["20260101_001_create_items.sql"],
		"20260102_001_add_item_name.sql": sqlFile(`ALTER TABLE items ADD COLUMN name TEXT NOT NULL DEFAULT '';`),
	}
	for i := 0; i < 2; i++ {
		s := mustOpen(t, dir, v2)
		if got := countRows(t, s, `SELECT count(*) FROM schema_migrations`); got != 2 {
			t.Fatalf("第 %d 次启动后 schema_migrations 应有 2 条，实际 %d", i+1, got)
		}
		s.Close()
	}
}

// 迁移失败时整个文件回滚，不留下半截表结构，也不记为已执行。
func TestMigrateRollsBackFailedFile(t *testing.T) {
	dir := t.TempDir()
	broken := fstest.MapFS{
		"20260101_001_broken.sql": sqlFile(`CREATE TABLE good (id INTEGER) STRICT; CREATE TABLE broken (`),
	}
	if s, err := open(dir, broken); err == nil {
		s.Close()
		t.Fatal("语法错误的迁移应导致打开失败")
	}

	s := mustOpen(t, dir, fstest.MapFS{})
	defer s.Close()
	if tableExists(t, s, "good") {
		t.Fatal("失败迁移里已执行的语句应被回滚")
	}
	if got := countRows(t, s, `SELECT count(*) FROM schema_migrations`); got != 0 {
		t.Fatalf("失败的迁移不应记为已执行，实际记录 %d 条", got)
	}
}

// 真实迁移在空库上能全部执行，重复启动不报错。
func TestOpenWithEmbeddedMigrationsTwice(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("第 %d 次启动失败: %v", i+1, err)
		}
		for _, table := range []string{"accounts", "api_keys", "usage_records"} {
			if !tableExists(t, s, table) {
				t.Fatalf("缺少表 %s", table)
			}
		}
		s.Close()
	}
}

// 库被更新的程序加过迁移时，旧程序打开它能报出不认识的迁移；MigrationsSince 按执行时间计数。
func TestUnknownMigrationsAndMigrationsSince(t *testing.T) {
	dir := t.TempDir()
	v2 := fstest.MapFS{
		"20260101_001_create_items.sql":  sqlFile(`CREATE TABLE items (id INTEGER PRIMARY KEY) STRICT;`),
		"20260102_001_add_item_name.sql": sqlFile(`ALTER TABLE items ADD COLUMN name TEXT NOT NULL DEFAULT '';`),
	}
	s := mustOpen(t, dir, v2)
	if u := s.UnknownMigrations(); len(u) != 0 {
		t.Fatalf("迁移都认识时不应报不认识的：%v", u)
	}
	if n, err := s.MigrationsSince(context.Background(), 0); err != nil || n != 2 {
		t.Fatalf("MigrationsSince(0) = %d, %v，应为 2", n, err)
	}
	if n, err := s.MigrationsSince(context.Background(), time.Now().Add(time.Hour).UnixMilli()); err != nil || n != 0 {
		t.Fatalf("MigrationsSince(一小时后) = %d, %v，应为 0", n, err)
	}
	s.Close()

	v1 := fstest.MapFS{"20260101_001_create_items.sql": v2["20260101_001_create_items.sql"]}
	s = mustOpen(t, dir, v1)
	defer s.Close()
	if u := s.UnknownMigrations(); len(u) != 1 || u[0] != "20260102_001_add_item_name.sql" {
		t.Fatalf("旧程序应报出 1 个不认识的迁移，实际 %v", u)
	}
}

// BackupTo 导出一份能独立打开、数据一致的库。
func TestBackupTo(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir, fstest.MapFS{
		"20260101_001_create_items.sql": sqlFile(`CREATE TABLE items (id INTEGER PRIMARY KEY) STRICT; INSERT INTO items VALUES (1), (2);`),
	})
	defer s.Close()
	dst := filepath.Join(t.TempDir(), "backup.db")
	if err := s.BackupTo(context.Background(), dst); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+dst)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM items`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("备份里 items 有 %d 行，%v，应为 2", n, err)
	}
	if err := s.BackupTo(context.Background(), dst); err == nil {
		t.Fatal("目标已存在时应报错，不覆盖")
	}
}
