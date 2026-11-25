package relay

import (
	"maps"
	"strings"

	"github.com/shiceai/agy2api/internal/antigravity"
)

// runtimeConfig 是执行层的运行时配置：管理端可调的调度与容错策略、档位、客户端名称过滤、系统提示词伪装和
// 请求处理开关。快照不可变，修改时复制一份、整体替换（见 updateConfig）。
//
// 一次请求开始时取一份快照、全程用它，中途改设置不会让同一个请求前后不一致：比如请求侧要了思考内容、响应侧
// 又把它去掉，或者空闲断开用的时长和用量里记的不一样。后台任务（额度刷新）每次执行时取最新的。
type runtimeConfig struct {
	policy Policy
	// defaultTier 是家族名请求不带思考参数时用的档位，空表示 antigravity.DefaultTier。
	defaultTier string
	// modelTiers 是按家族单独设置的档位，优先于 defaultTier。只读，替换时整体换新的。
	modelTiers map[string]string
	// clientFilter 是客户端名称过滤器，nil 表示不过滤。
	clientFilter *antigravity.ClientFilter
	// systemPrompt 是系统提示词伪装配置，nil 表示不注入。
	systemPrompt *systemPromptConfig
	// remoteImages 为真时把请求里的 http(s) 图片 URL 下载后内联；hideThoughts 为真时不把思考内容返回给客户端。
	remoteImages bool
	hideThoughts bool
}

// systemPromptConfig 是系统提示词伪装的生效配置。
type systemPromptConfig struct {
	mode     antigravity.SystemPromptMode
	template string
}

// config 返回当前的运行时配置快照，只读。
func (r *Relay) config() *runtimeConfig {
	return r.cfg.Load()
}

// updateConfig 复制当前快照、用 fn 修改后整体替换。修改串行执行，fn 在锁内运行。
func (r *Relay) updateConfig(fn func(*runtimeConfig)) {
	r.cfgMu.Lock()
	defer r.cfgMu.Unlock()
	next := *r.cfg.Load()
	fn(&next)
	r.cfg.Store(&next)
}

// configuredTier 返回家族名请求不带思考参数时的档位：该家族单独设置的优先，否则用默认档位。
func (c *runtimeConfig) configuredTier(requested string) string {
	if t := c.modelTiers[requested]; t != "" {
		return t
	}
	if c.defaultTier != "" {
		return c.defaultTier
	}
	return antigravity.DefaultTier
}

// tier 返回家族名选档用的档位：请求带思考参数时按参数，否则按配置（见 configuredTier）。
func (c *runtimeConfig) tier(requested string, body map[string]any) string {
	if t := antigravity.RequestedTier(body); t != "" {
		return t
	}
	return c.configuredTier(requested)
}

// SetDefaultTier 设置家族名请求不带思考参数时用的档位，立即生效。
func (r *Relay) SetDefaultTier(tier string) {
	r.updateConfig(func(c *runtimeConfig) { c.defaultTier = tier })
}

// DefaultTier 返回当前默认档位，没设置过时为 antigravity.DefaultTier。
func (r *Relay) DefaultTier() string {
	if t := r.config().defaultTier; t != "" {
		return t
	}
	return antigravity.DefaultTier
}

// SetModelTiers 设置按家族单独配置的档位（家族名 → 档位），整体替换，立即生效。
func (r *Relay) SetModelTiers(tiers map[string]string) {
	tiers = maps.Clone(tiers)
	r.updateConfig(func(c *runtimeConfig) { c.modelTiers = tiers })
}

// ModelTiers 返回按家族单独配置的档位副本。
func (r *Relay) ModelTiers() map[string]string {
	if m := maps.Clone(r.config().modelTiers); m != nil {
		return m
	}
	return map[string]string{}
}

// SetClientFilter 设置客户端名称过滤器，立即生效；nil 或无有效规则时不过滤。
func (r *Relay) SetClientFilter(f *antigravity.ClientFilter) {
	r.updateConfig(func(c *runtimeConfig) { c.clientFilter = f })
}

// SetSystemPrompt 设置系统提示词伪装，立即生效；模板为空时不注入。
func (r *Relay) SetSystemPrompt(mode antigravity.SystemPromptMode, template string) {
	var sp *systemPromptConfig
	if strings.TrimSpace(template) != "" {
		sp = &systemPromptConfig{mode: mode, template: template}
	}
	r.updateConfig(func(c *runtimeConfig) { c.systemPrompt = sp })
}

// SetRequestHandling 设置请求处理开关，立即生效：是否内联远程图片、是否把思考内容返回给客户端。
func (r *Relay) SetRequestHandling(remoteImages, returnThoughts bool) {
	r.updateConfig(func(c *runtimeConfig) {
		c.remoteImages = remoteImages
		c.hideThoughts = !returnThoughts
	})
}
