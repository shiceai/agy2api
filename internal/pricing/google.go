package pricing

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	// 限时价的日期按美国太平洋时间理解，Windows 和精简镜像上没有系统时区数据，内嵌一份。
	_ "time/tzdata"
)

// Google AI Studio 价格页（请求头 Accept: text/markdown 时返回 Markdown）。每个型号一节，标题带型号 ID，
// 取 Standard 标签页里付费层（Paid Tier, per 1M tokens in USD）的输入价、输出价和上下文缓存价。
// 页面格式参考 shice 项目对同一页面的调研和解析规则。
const googlePricingURL = "https://aistudio.google.com/docs/pricing"

var (
	googleHeading    = regexp.MustCompile(`^## (.+?)\s+\{% id="([^"]+)" %\}`)
	googleMarkup     = regexp.MustCompile(`\{%[^%]*%\}`)
	googleRowSep     = regexp.MustCompile(`\n\s*---\s*\n`)
	googleCellStart  = regexp.MustCompile(`(?m)^\s*\*\s`)
	googleLineBreak  = regexp.MustCompile(`\s*\\\\\s*`)
	googleStorage    = regexp.MustCompile(`\$[0-9.]+ / 1,000,000 tokens per hour \(storage price\)(?: (?:through|starting) [A-Z][a-z]+ [0-9]{1,2}, [0-9]{4}\.?)?`)
	googleCacheNote  = regexp.MustCompile(`Implicit caching only \(explicit caching is not supported\)\.?`)
	googlePlain      = regexp.MustCompile(`^\$([0-9]+(?:\.[0-9]+)?)$`)
	googleTextFirst  = regexp.MustCompile(`^\$([0-9]+(?:\.[0-9]+)?) \(text[^)]*\)(?: \$[0-9]+(?:\.[0-9]+)? \(audio\))?$`)
	googleTiered     = regexp.MustCompile(`^\$([0-9]+(?:\.[0-9]+)?), prompts <= ([0-9]+)k tokens \$([0-9]+(?:\.[0-9]+)?), prompts > ([0-9]+)k(?: tokens)?$`)
	googleDated      = regexp.MustCompile(`^\$([0-9]+(?:\.[0-9]+)?)(?: \(text[^)]*\))? through ([A-Z][a-z]+ [0-9]{1,2}, [0-9]{4})\. \$([0-9]+(?:\.[0-9]+)?)(?: \(text[^)]*\))? starting ([A-Z][a-z]+ [0-9]{1,2}, [0-9]{4})\.?$`)
	officialModelRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
	googleZone, _    = time.LoadLocation("America/Los_Angeles")
	errNoGoogleModel = errors.New("价格页找不到型号小节，页面可能改版")
)

// googleCell 是一个单元格解析出的单价：基础价；按提示长度分档时 threshold 和 over；
// 带期限时 base 为标准价、promo 为限时价、promoEnd 为恢复标准价的时刻。
type googleCell struct {
	base      *int64
	threshold int64
	over      *int64
	promo     *int64
	promoEnd  time.Time
}

// parseGoogle 解析 Google AI Studio 价格页。只认单一价格、按提示长度分两档、按模态分开且第一项是文字、
// 带期限（某日前限时价，次日起标准价）四种写法；按图片或时长计价、输出不是文字的型号计为读不出。
func parseGoogle(data []byte) ([]Entry, []Skip, error) {
	text := strings.ReplaceAll(string(data), "\r", "")
	var entries []Entry
	var skipped []Skip
	sections := 0
	for _, section := range strings.Split(text, "\n## ") {
		section = "## " + strings.TrimPrefix(section, "## ")
		heading := googleHeading.FindStringSubmatch(section)
		if heading == nil {
			continue
		}
		name, model := strings.TrimSpace(heading[1]), heading[2]
		// 型号小节都链接到型号页，工具、说明等小节没有。
		if !officialModelRe.MatchString(model) || !strings.Contains(section, "(/docs/models/"+model+")") {
			continue
		}
		sections++
		price, reason := parseGoogleSection(section)
		if reason != "" {
			skipped = append(skipped, Skip{Model: model, Reason: reason})
			continue
		}
		entries = append(entries, Entry{Model: model, Source: SourceOfficial, Vendor: VendorGoogle, Name: name, Price: price})
	}
	if sections == 0 {
		return nil, nil, errNoGoogleModel
	}
	return entries, skipped, nil
}

// parseGoogleSection 解析一个型号小节的 Standard 价格表；reason 不为空表示读不出。
func parseGoogleSection(section string) (Price, string) {
	_, standard, found := strings.Cut(section, `{% tab title="Standard" %}`)
	if !found {
		return Price{}, "没有 Standard 档位的价格"
	}
	standard, _, _ = strings.Cut(standard, "{% /tab %}")
	rows := googleTable(standard)
	if len(rows) < 2 {
		return Price{}, "找不到价格表"
	}
	paid := -1
	for i, cell := range rows[0] {
		if strings.HasPrefix(cell, "Paid Tier") && strings.Contains(cell, "per 1M tokens in USD") {
			paid = i
		}
	}
	if paid < 0 {
		return Price{}, "找不到付费层每百万 token 的价格列"
	}
	var inputCell, outputCell, cacheCell string
	for _, row := range rows[1:] {
		if paid >= len(row) {
			continue
		}
		switch label := strings.ToLower(row[0]); {
		case strings.HasPrefix(label, "input price"):
			inputCell = row[paid]
		case strings.HasPrefix(label, "output price"):
			outputCell = row[paid]
		case strings.HasPrefix(label, "context caching price"):
			cacheCell = row[paid]
		}
	}
	input, r1 := parseGoogleCell(inputCell, false)
	output, r2 := parseGoogleCell(outputCell, false)
	cache, r3 := parseGoogleCell(cacheCell, true)
	for _, r := range []string{r1, r2, r3} {
		if r != "" {
			return Price{}, r
		}
	}
	if input.base == nil || output.base == nil {
		return Price{}, "价格格式不认识"
	}

	p := Price{Rates: Rates{Input: *input.base, Output: *output.base, CacheRead: cache.base}}
	threshold := max(input.threshold, output.threshold, cache.threshold)
	if threshold > 0 {
		over := Rates{Input: orBase(input), Output: orBase(output)}
		if cache.base != nil {
			v := *cache.base
			if cache.over != nil {
				v = *cache.over
			}
			over.CacheRead = &v
		}
		for _, c := range []googleCell{input, output, cache} {
			if c.threshold > 0 && c.threshold != threshold {
				return Price{}, "分档的阈值不一致"
			}
		}
		p.Tier = &Tier{Threshold: threshold, Rates: over}
	}
	// 带期限的单元格期限必须一致；没写期限的项在限时期间沿用标准价。
	var end time.Time
	for _, c := range []googleCell{input, output, cache} {
		if c.promo == nil {
			continue
		}
		if !end.IsZero() && !end.Equal(c.promoEnd) {
			return Price{}, "限时价的期限不一致"
		}
		end = c.promoEnd
	}
	if !end.IsZero() {
		if p.Tier != nil {
			return Price{}, "限时价带分档"
		}
		promo := Rates{Input: orPromo(input), Output: orPromo(output), CacheRead: cache.base}
		if cache.promo != nil {
			promo.CacheRead = cache.promo
		}
		p.Promo = &Promo{End: end.UnixMilli(), Rates: promo}
	}
	if !p.Valid() {
		return Price{}, "价格不合法"
	}
	return p, ""
}

func orBase(c googleCell) int64 {
	if c.over != nil {
		return *c.over
	}
	return *c.base
}

func orPromo(c googleCell) int64 {
	if c.promo != nil {
		return *c.promo
	}
	return *c.base
}

// googleTable 解析 Markdoc 表格：行之间以 --- 分隔，单元格以 * 开头。
func googleTable(text string) [][]string {
	_, body, found := strings.Cut(text, "{% table %}")
	if !found {
		return nil
	}
	body, _, _ = strings.Cut(body, "{% /table %}")
	var rows [][]string
	for _, block := range googleRowSep.Split(body, -1) {
		var cells []string
		for _, part := range googleCellStart.Split(block, -1) {
			if cell := strings.Join(strings.Fields(googleMarkup.ReplaceAllString(part, "")), " "); cell != "" {
				cells = append(cells, cell)
			}
		}
		if len(cells) > 0 {
			rows = append(rows, cells)
		}
	}
	return rows
}

// parseGoogleCell 解析一个价格单元格；reason 不为空表示读不出。缓存价可以没有（Not available、- 或空）。
func parseGoogleCell(cell string, optional bool) (googleCell, string) {
	cell = googleLineBreak.ReplaceAllString(cell, " ")
	cell = googleStorage.ReplaceAllString(cell, "")
	cell = googleCacheNote.ReplaceAllString(cell, "")
	cell = strings.Join(strings.Fields(cell), " ")
	switch {
	case cell == "" || cell == "-" || cell == "Not available":
		if optional {
			return googleCell{}, ""
		}
		return googleCell{}, "缺少输入或输出价"
	case strings.Contains(cell, " through ") || strings.Contains(cell, " starting "):
		m := googleDated.FindStringSubmatch(cell)
		if m == nil {
			return googleCell{}, "价格格式不认识"
		}
		end, ok := googlePromoEnd(m[2], m[4])
		if !ok {
			return googleCell{}, "限时价的日期不认识"
		}
		return googleCell{base: dollars(m[3]), promo: dollars(m[1]), promoEnd: end}, ""
	}
	if m := googlePlain.FindStringSubmatch(cell); m != nil {
		return googleCell{base: dollars(m[1])}, ""
	}
	if m := googleTextFirst.FindStringSubmatch(cell); m != nil {
		return googleCell{base: dollars(m[1])}, ""
	}
	if m := googleTiered.FindStringSubmatch(cell); m != nil && m[2] == m[4] {
		k, _ := strconv.ParseInt(m[2], 10, 64)
		return googleCell{base: dollars(m[1]), threshold: k * 1000, over: dollars(m[3])}, ""
	}
	return googleCell{}, "价格格式不认识"
}

// googlePromoEnd 把"through 某日""starting 次日"换算成恢复标准价的时刻：次日 0 点（美国太平洋时间）。
// 两个日期必须正好差一天。
func googlePromoEnd(through, starting string) (time.Time, bool) {
	if googleZone == nil {
		return time.Time{}, false
	}
	last, err1 := time.ParseInLocation("January 2, 2006", through, googleZone)
	start, err2 := time.ParseInLocation("January 2, 2006", starting, googleZone)
	if err1 != nil || err2 != nil || !last.AddDate(0, 0, 1).Equal(start) {
		return time.Time{}, false
	}
	return start, true
}

// dollars 把 "1.25" 这样的美元数换成百万分之一美元的整数。
func dollars(text string) *int64 {
	v, err := strconv.ParseFloat(strings.ReplaceAll(text, ",", ""), 64)
	if err != nil || v < 0 || v > 1e6 {
		return nil
	}
	micro := int64(v*1e6 + 0.5)
	return &micro
}
