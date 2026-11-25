package pricing

import (
	"strings"

	"github.com/shiceai/agy2api/internal/antigravity"
)

// 价格来源和官方来源的厂商。
const (
	SourceOfficial = "official"
	SourceManual   = "manual"

	VendorGoogle    = "google"
	VendorAnthropic = "anthropic"
)

// Entry 是价格表里的一条价格。
type Entry struct {
	// Model 是价格对应的型号：官方价为官方型号 ID，手动价为号池里的模型名或家族名。
	Model  string `json:"model"`
	Source string `json:"source"`
	Vendor string `json:"vendor,omitempty"`
	// Name 是官方价格页上的显示名。
	Name      string `json:"name,omitempty"`
	Price     Price  `json:"price"`
	UpdatedAt int64  `json:"updated_at"`
}

// Skip 是官方价格页上读不出价格的型号和原因。
type Skip struct {
	Model  string `json:"model"`
	Reason string `json:"reason"`
}

// aliases 是号池里的家族名与官方型号 ID 不一致的对应（依据：同一个模型在官方价格页上的 ID）。
var aliases = map[string]string{
	"gemini-3-flash":            "gemini-3-flash-preview",
	"gemini-2.5-flash-thinking": "gemini-2.5-flash",
}

// Book 是某一时刻的价格表快照，只读，可并发使用。
type Book struct {
	manual   map[string]Entry
	official map[string]Entry
}

// NewBook 用价格列表构造快照。
func NewBook(entries []Entry) *Book {
	b := &Book{manual: map[string]Entry{}, official: map[string]Entry{}}
	for _, e := range entries {
		if e.Source == SourceManual {
			b.manual[e.Model] = e
		} else {
			b.official[e.Model] = e
		}
	}
	return b
}

// Family 返回模型 ID 去掉档位后缀（-low、-medium、-high、-xhigh）后的家族名；没有档位时原样返回。
func Family(model string) string {
	if t := antigravity.TierOf(model); t != "" {
		return strings.TrimSuffix(model, "-"+t)
	}
	return model
}

// Candidates 返回查价时依次尝试的名字：模型 ID 本身、去掉档位的家族名，以及它们在别名表里对应的官方型号。
func Candidates(model string) []string {
	var out []string
	add := func(name string) {
		for _, n := range out {
			if n == name {
				return
			}
		}
		out = append(out, name)
	}
	for _, name := range []string{model, Family(model)} {
		add(name)
		if a, ok := aliases[name]; ok {
			add(a)
		}
	}
	return out
}

// Find 返回模型（号池里的模型 ID，可带档位后缀）的价格：按 Candidates 的顺序，同一个名字手动价优先于官方价。
func (b *Book) Find(model string) (Entry, bool) {
	if b == nil || model == "" {
		return Entry{}, false
	}
	for _, name := range Candidates(model) {
		if e, ok := b.manual[name]; ok {
			return e, true
		}
		if e, ok := b.official[name]; ok {
			return e, true
		}
	}
	return Entry{}, false
}

// Official 返回官方价（不看手动价），手动价管理里显示"恢复后的价格"用。
func (b *Book) Official(model string) (Entry, bool) {
	if b == nil {
		return Entry{}, false
	}
	for _, name := range Candidates(model) {
		if e, ok := b.official[name]; ok {
			return e, true
		}
	}
	return Entry{}, false
}

// Entries 返回全部价格（手动价在前），管理端列表用。
func (b *Book) Entries() []Entry {
	out := make([]Entry, 0, len(b.manual)+len(b.official))
	for _, e := range b.manual {
		out = append(out, e)
	}
	for _, e := range b.official {
		out = append(out, e)
	}
	return out
}
