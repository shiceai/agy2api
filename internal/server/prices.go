package server

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shiceai/agy2api/internal/pricing"
	"github.com/shiceai/agy2api/internal/store"
)

// priceRow 是管理端价格列表的一行：号池里的一个模型家族（没有档位的模型就是它自己），或号池里已经没有、
// 但还留着手动价的模型。
type priceRow struct {
	// Model 是这一行的名字，手动设价就设在它上面。
	Model string `json:"model"`
	// IDs 是号池里属于它的模型 ID（各档位）。
	IDs []string `json:"ids"`
	// Price 是生效的价格（手动价优先），没有价格时为空；Official 是官方价，有手动价时用来显示恢复后的价格。
	Price    *pricing.Entry `json:"price"`
	Official *pricing.Entry `json:"official"`
}

// pricesView 是价格页的数据。
type pricesView struct {
	Status   pricing.Status `json:"status"`
	AutoSync bool           `json:"auto_sync"`
	Models   []priceRow     `json:"models"`
}

func (s *Service) pricesView(ctx context.Context) (*pricesView, error) {
	status, err := s.prices.Status(ctx)
	if err != nil {
		return nil, err
	}
	book := s.prices.Book()
	rows := map[string]*priceRow{}
	for _, m := range s.relay.Models() {
		family := pricing.Family(m.ID)
		row := rows[family]
		if row == nil {
			row = &priceRow{Model: family}
			rows[family] = row
		}
		row.IDs = append(row.IDs, m.ID)
	}
	for _, e := range book.Entries() {
		if e.Source == pricing.SourceManual && rows[e.Model] == nil {
			rows[e.Model] = &priceRow{Model: e.Model, IDs: []string{}}
		}
	}
	out := make([]priceRow, 0, len(rows))
	for _, row := range rows {
		sort.Strings(row.IDs)
		if e, ok := book.Find(row.Model); ok {
			row.Price = &e
		}
		if e, ok := book.Official(row.Model); ok {
			row.Official = &e
		}
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return &pricesView{Status: status, AutoSync: s.cfg.PriceSync, Models: out}, nil
}

func (s *Service) writePrices(w http.ResponseWriter, r *http.Request) {
	v, err := s.pricesView(r.Context())
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) adminGetPrices(w http.ResponseWriter, r *http.Request) {
	s.writePrices(w, r)
}

// adminSyncPrices 立即从官方价格页同步一次（不受 AGY2API_PRICE_SYNC 影响），各来源的结果在返回的 status 里。
func (s *Service) adminSyncPrices(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if _, err := s.prices.Sync(ctx); err != nil {
		slog.ErrorContext(ctx, "同步官方价格失败", "err", err)
		adminError(w, http.StatusInternalServerError, "保存价格失败："+err.Error())
		return
	}
	s.writePrices(w, r)
}

// manualPriceRequest 是手动设价，单位美元每百万 token；cache_read 为空时缓存命中按输入价算。
type manualPriceRequest struct {
	Input     float64  `json:"input"`
	Output    float64  `json:"output"`
	CacheRead *float64 `json:"cache_read"`
}

// adminSetPrice 手动设置一个模型（号池里的模型名或家族名）的价格，优先于官方价；之后补算还没算费用的用量记录。
func (s *Service) adminSetPrice(w http.ResponseWriter, r *http.Request) {
	model := chi.URLParam(r, "model")
	var req manualPriceRequest
	if !decodeAdminJSON(w, r, 1<<10, &req) {
		return
	}
	p := pricing.Price{Rates: pricing.Rates{Input: microDollars(req.Input), Output: microDollars(req.Output)}}
	if req.CacheRead != nil {
		v := microDollars(*req.CacheRead)
		p.CacheRead = &v
	}
	err := s.prices.SetManual(r.Context(), model, p)
	switch {
	case errors.Is(err, pricing.ErrInvalidModel):
		adminError(w, http.StatusBadRequest, "模型名只能包含字母、数字和 . _ : -")
		return
	case errors.Is(err, pricing.ErrInvalidPrice):
		adminError(w, http.StatusBadRequest, "单价不能为负，输入和输出不能都为 0，最多 100 万美元")
		return
	case err != nil:
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已手动设置模型价格", "model", model, "input", req.Input, "output", req.Output)
	s.writePrices(w, r)
}

// adminDeletePrice 删除手动价，恢复官方价。
func (s *Service) adminDeletePrice(w http.ResponseWriter, r *http.Request) {
	model := chi.URLParam(r, "model")
	err := s.prices.DeleteManual(r.Context(), model)
	if errors.Is(err, store.ErrNotFound) {
		adminError(w, http.StatusNotFound, "这个模型没有手动设置的价格")
		return
	}
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已删除手动设置的模型价格", "model", model)
	s.writePrices(w, r)
}

// microDollars 把美元换成百万分之一美元；负数、超过 100 万美元或不是数时返回 -1，由价格校验拒绝。
func microDollars(v float64) int64 {
	if math.IsNaN(v) || v < 0 || v > 1e6 {
		return -1
	}
	return int64(math.Round(v * 1e6))
}
