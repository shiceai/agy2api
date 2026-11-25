package pricing

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Anthropic 价格页的 Markdown 版本（地址以 .md 结尾）。读 "Model pricing" 一节的价格表：基础输入、缓存命中、输出；
// 缓存写入不读（Antigravity 返回的用量里没有缓存写入数）。页面格式参考 shice 项目的调研。
const anthropicPricingURL = "https://platform.claude.com/docs/en/about-claude/pricing.md"

var (
	anthropicSup  = regexp.MustCompile(`<sup>.*?</sup>`)
	anthropicLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	// 型号名和括号里的说明，如 "Claude Haiku 5.5 (for prompts up to 100,000 tokens)"。
	anthropicName  = regexp.MustCompile(`^(Claude [A-Za-z]+ [0-9.]+)(?: \((.+)\))?$`)
	anthropicTier  = regexp.MustCompile(`^for prompts (up to|over) ([0-9,]+) tokens$`)
	anthropicPrice = regexp.MustCompile(`^\$([0-9][0-9,]*(?:\.[0-9]+)?) / MTok$`)
)

// parseAnthropic 解析 Anthropic 价格页。型号 ID 按显示名换算（"Claude Opus 5.5" → claude-opus-5-5），
// 与 Antigravity 的命名一致；已退役（retired）的型号不读；"for prompts up to / over N tokens" 两行合成一档分档价。
func parseAnthropic(data []byte) ([]Entry, []Skip, error) {
	text := strings.ReplaceAll(string(data), "\r", "")
	_, section, found := strings.Cut(text, "\n## Model pricing")
	if !found {
		return nil, nil, errors.New("价格页找不到 Model pricing 一节，页面可能改版")
	}
	if i := strings.Index(section, "\n## "); i >= 0 {
		section = section[:i]
	}
	rows := markdownTable(section)
	if len(rows) < 2 {
		return nil, nil, errors.New("价格页找不到型号价格表，页面可能改版")
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	inCol, okIn := col["Base input tokens"]
	hitCol, okHit := col["Cache hits and refreshes"]
	outCol, okOut := col["Output tokens"]
	if col["Model"] != 0 || !okIn || !okHit || !okOut {
		return nil, nil, errors.New("价格表的列和预期不符，页面可能改版")
	}

	type pending struct {
		name  string
		base  *Rates
		upTo  int64 // "for prompts up to N tokens" 的 N
		tier  *Tier
		issue string
	}
	byModel := map[string]*pending{}
	var order []string
	for _, row := range rows[1:] {
		m := anthropicName.FindStringSubmatch(row[0])
		if m == nil {
			continue
		}
		name, note := m[1], m[2]
		if strings.Contains(note, "retired") {
			continue
		}
		model := strings.NewReplacer(" ", "-", ".", "-").Replace(strings.ToLower(name))
		p := byModel[model]
		if p == nil {
			p = &pending{name: name}
			byModel[model] = p
			order = append(order, model)
		}
		input, ok1 := anthropicDollars(cellOf(row, inCol))
		hit, ok2 := anthropicDollars(cellOf(row, hitCol))
		output, ok3 := anthropicDollars(cellOf(row, outCol))
		if !ok1 || !ok2 || !ok3 || input == nil || output == nil {
			p.issue = "价格格式不认识"
			continue
		}
		rates := Rates{Input: *input, Output: *output, CacheRead: hit}
		if t := anthropicTier.FindStringSubmatch(note); t != nil {
			n, _ := strconv.ParseInt(strings.ReplaceAll(t[2], ",", ""), 10, 64)
			if t[1] == "over" {
				if p.tier != nil {
					p.issue = "长提示的价格重复"
				}
				p.tier = &Tier{Threshold: n, Rates: rates}
				continue
			}
			p.upTo = n
		} else if note != "" && !strings.Contains(note, "limited availability") {
			p.issue = "型号说明不认识"
			continue
		}
		if p.base != nil {
			p.issue = "同一型号有多行价格"
			continue
		}
		p.base = &rates
	}

	var entries []Entry
	var skipped []Skip
	for _, model := range order {
		p := byModel[model]
		switch {
		case p.issue != "":
		case p.base == nil:
			p.issue = "只有长提示的价格"
		case (p.tier == nil) != (p.upTo == 0) || p.tier != nil && p.tier.Threshold != p.upTo:
			p.issue = "长提示的价格不完整"
		}
		price := Price{}
		if p.issue == "" {
			price = Price{Rates: *p.base, Tier: p.tier}
			if !price.Valid() {
				p.issue = "价格不合法"
			}
		}
		if p.issue != "" {
			skipped = append(skipped, Skip{Model: model, Reason: p.issue})
			continue
		}
		entries = append(entries, Entry{Model: model, Source: SourceOfficial, Vendor: VendorAnthropic, Name: p.name, Price: price})
	}
	if len(entries) == 0 {
		return nil, nil, errors.New("价格表读不出任何型号，页面可能改版")
	}
	return entries, skipped, nil
}

// markdownTable 读一节里的第一张 Markdown 表格（含表头，不含分隔行），单元格去掉上标和链接，只留文字。
func markdownTable(section string) [][]string {
	var rows [][]string
	started := false
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			if started {
				break
			}
			continue
		}
		started = true
		parts := strings.Split(strings.Trim(line, "|"), "|")
		cells := make([]string, len(parts))
		separator := true
		for i, part := range parts {
			cell := anthropicLink.ReplaceAllString(anthropicSup.ReplaceAllString(part, ""), "$1")
			cells[i] = strings.Join(strings.Fields(cell), " ")
			if strings.Trim(cells[i], ":-") != "" {
				separator = false
			}
		}
		if !separator {
			rows = append(rows, cells)
		}
	}
	return rows
}

func cellOf(row []string, i int) string {
	if i < len(row) {
		return row[i]
	}
	return ""
}

// anthropicDollars 解析 "$4 / MTok"；"-" 或空为没有这项（nil, true）。
func anthropicDollars(cell string) (*int64, bool) {
	if cell == "" || cell == "-" {
		return nil, true
	}
	m := anthropicPrice.FindStringSubmatch(cell)
	if m == nil {
		return nil, false
	}
	v := dollars(m[1])
	return v, v != nil
}
