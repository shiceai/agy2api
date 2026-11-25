package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/shiceai/agy2api/internal/store"
)

// jsonSetting 是 settings 表里一个键下的一组设置，存成 JSON、整体替换（调度、客户端名称过滤、系统提示词、
// 请求处理、用量保留）。几组共用同一套规则：
//   - 读：在默认值上解码，库里没有的字段取默认值；不是合法 JSON 或校验不过时记警告，整体用默认值。
//     启动时生效的和管理端看到的是同一个值。
//   - 写：在 base 上解码请求体（缺的字段取 base 的值），校验不过返回 400，写库；生效和记日志由调用方做。
type jsonSetting[T any] struct {
	key  string
	name string // 日志里的名字，如"调度设置"
	// limit 是管理端保存请求体的上限（字节）。
	limit    int64
	defaults func() T
	// validate 检查取值，错误说明直接返回给管理端；nil 表示不校验。
	validate func(T) error
}

func (c jsonSetting[T]) check(v T) error {
	if c.validate == nil {
		return nil
	}
	return c.validate(v)
}

// load 读库里的设置（规则见类型说明）。只有读库出错时返回 error，此时值为默认值。
func (c jsonSetting[T]) load(ctx context.Context, st *store.Store) (T, error) {
	raw, ok, err := st.GetSetting(ctx, c.key)
	if err != nil || !ok {
		return c.defaults(), err
	}
	v := c.defaults()
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		slog.WarnContext(ctx, "库里的"+c.name+"不是合法 JSON，改用默认值", "err", err)
		return c.defaults(), nil
	}
	if err := c.check(v); err != nil {
		slog.WarnContext(ctx, "库里的"+c.name+"不合法，改用默认值", "err", err)
		return c.defaults(), nil
	}
	return v, nil
}

// update 处理管理端的保存请求：在 base 上解码请求体、校验、写库。失败时已写回错误响应，ok 为 false。
func (c jsonSetting[T]) update(w http.ResponseWriter, r *http.Request, st *store.Store, base T) (v T, ok bool) {
	v = base
	if !decodeAdminJSON(w, r, c.limit, &v) {
		return v, false
	}
	if err := c.check(v); err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return v, false
	}
	raw, err := json.Marshal(v)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return v, false
	}
	if err := st.SetSetting(r.Context(), c.key, string(raw)); err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return v, false
	}
	return v, true
}
