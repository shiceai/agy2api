package ddl

import (
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"testing"
	"time"
)

var migrationName = regexp.MustCompile(`^(\d{8})_(\d{3})_[a-z0-9]+(?:_[a-z0-9]+)*\.sql$`)

// TestMigrationNames 检查文件命名：YYYYMMDD_NNN_description.sql，
// 日期合法，同日序号从 001 起连续递增。
func TestMigrationNames(t *testing.T) {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("没有迁移脚本")
	}
	seqByDate := map[string][]int{}
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			t.Errorf("迁移文件名不合规：%s（应为 YYYYMMDD_NNN_description.sql，描述用小写 snake_case）", e.Name())
			continue
		}
		if _, err := time.Parse("20060102", m[1]); err != nil {
			t.Errorf("迁移文件日期不合法：%s", e.Name())
		}
		seq, _ := strconv.Atoi(m[2])
		seqByDate[m[1]] = append(seqByDate[m[1]], seq)
	}
	for date, seqs := range seqByDate {
		sort.Ints(seqs)
		for i, seq := range seqs {
			if seq != i+1 {
				t.Errorf("%s 的迁移序号应从 001 起连续递增，实际为 %v", date, seqs)
				break
			}
		}
	}
}
