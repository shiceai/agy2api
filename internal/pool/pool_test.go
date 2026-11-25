package pool

import (
	"context"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/cryptox"
	"github.com/shiceai/agy2api/internal/store"
)

type fixture struct {
	t     *testing.T
	store *store.Store
	enc   *cryptox.Cipher
	pool  *Pool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	enc, err := cryptox.New([]byte("test-key-material"))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return &fixture{t: t, store: st, enc: enc, pool: New(st, enc)}
}

// addAccount 入库一个账号；models 为空表示目录未知。
func (f *fixture) addAccount(email, projectID, status string, models ...string) int64 {
	f.t.Helper()
	catalog := make([]antigravity.ModelInfo, 0, len(models))
	for _, m := range models {
		catalog = append(catalog, antigravity.ModelInfo{ID: m})
	}
	a := &store.Account{
		Email:        email,
		ProjectID:    projectID,
		RefreshToken: "rt-" + email,
		Status:       status,
		ModelsJSON:   antigravity.MarshalModels(catalog),
	}
	if err := f.store.CreateAccount(context.Background(), f.enc, a); err != nil {
		f.t.Fatalf("create account: %v", err)
	}
	return a.ID
}

func (f *fixture) reload() {
	f.t.Helper()
	if err := f.pool.Reload(context.Background()); err != nil {
		f.t.Fatalf("reload: %v", err)
	}
}

// pick 选一次号并立即归还名额，返回账号 ID 和解析出的模型。
func (f *fixture) pick(model, tier string) (int64, string, error) {
	f.t.Helper()
	lease, err := f.pool.Select(model, tier, nil, 0)
	if err != nil {
		return 0, "", err
	}
	lease.Release()
	return lease.Account.ID, lease.Model, nil
}

// selectIDs 连续选号 n 次，返回选中的账号 ID 次数。
func (f *fixture) selectIDs(model string, n int) map[int64]int {
	f.t.Helper()
	got := map[int64]int{}
	for i := 0; i < n; i++ {
		id, _, err := f.pick(model, "medium")
		if err != nil {
			f.t.Fatalf("select %s: %v", model, err)
		}
		got[id]++
	}
	return got
}

func (f *fixture) cooldown(id int64, model string, d time.Duration) {
	f.pool.Cooldown(context.Background(), id, time.Now().Add(d), "rate_limit", model)
}
