package store

import (
	"testing"
	"testing/fstest"
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
