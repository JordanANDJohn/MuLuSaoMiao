package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// GenOptions 路径生成配置
type GenOptions struct {
	Combine  string   // none | prefix | suffix | both（多字典组合模式）
	SmartExt bool     // 智能扩展名：对每个路径词追加 .bak/.bin/~/…
	FuzzTpls []string // 模糊测试模板，如 /api/{FUZZ}/v1（可多个）
	Keywords []string // -keyword：域名/产品名（自动生成变体）
	Products []string // 产品名（{PRODUCT} 占位符取值 + 关键词生成）
	Host     string   // 目标主机名（{HOST} 占位符取值）
	MaxPaths int      // 最大生成路径数（0 不限制）
}

// PathGen 路径生成器：字典 → 完整候选路径列表
type PathGen struct {
	Dict *Dictionary
	Opts GenOptions
}

// Generate 生成全部候选路径（以 / 开头，相对 base URL；模板可产出完整 URL）。
// 生成顺序：基础词 → 关键词变体 → 智能扩展名 → 多字典组合 → fuzz 模板。
func (g *PathGen) Generate() []string {
	set := map[string]struct{}{}
	add := func(p string) {
		if p == "" {
			return
		}
		set[p] = struct{}{}
	}

	words := append([]string{}, g.Dict.Words...)
	words = append(words, g.Opts.Keywords...)
	words = unique(words)

	// 1) 基础路径 + 2) 智能扩展名
	for _, w := range words {
		add("/" + w)
		add("/" + w + "/")
		if g.Opts.SmartExt {
			for _, e := range g.Dict.Extensions {
				add("/" + w + e)
			}
		}
	}

	// 3) 多字典组合：前缀/后缀插入
	havePrefix := len(g.Dict.Prefixes) > 0
	haveSuffix := len(g.Dict.Suffixes) > 0
	combine := strings.ToLower(strings.TrimSpace(g.Opts.Combine))
	doPrefix := (combine == "prefix" || combine == "both") && havePrefix
	doSuffix := (combine == "suffix" || combine == "both") && haveSuffix

	if doPrefix || doSuffix {
		// 先取"被组合"的字典：优先使用用户传入的前缀/后缀字典作为另一侧时，
		// 用主词桶（含关键词变体）；若前缀字典不存在，回退主词。
		combineSeeds := words
		if combine == "prefix" && havePrefix {
			for _, p := range g.Dict.Prefixes {
				for _, w := range combineSeeds {
					add("/" + p + "_" + w)
					add("/" + p + "_" + w + "/")
				}
			}
		}
		if combine == "suffix" && haveSuffix {
			for _, w := range combineSeeds {
				for _, s := range g.Dict.Suffixes {
					add("/" + w + "_" + s)
					add("/" + w + "_" + s + "/")
				}
			}
		}
		if combine == "both" {
			pfx := g.Dict.Prefixes
			sfx := g.Dict.Suffixes
			if !havePrefix {
				pfx = []string{""}
			}
			if !haveSuffix {
				sfx = []string{""}
			}
			for _, p := range pfx {
				for _, w := range combineSeeds {
					for _, s := range sfx {
						part := w
						if p != "" {
							part = p + "_" + part
						}
						if s != "" {
							part = part + "_" + s
						}
						add("/" + part)
						add("/" + part + "/")
					}
				}
			}
		}
	}

	// 4) fuzz 模板展开
	for _, tpl := range g.Opts.FuzzTpls {
		for _, p := range g.expandTemplate(tpl) {
			add(p)
		}
	}

	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	if g.Opts.MaxPaths > 0 && len(paths) > g.Opts.MaxPaths {
		fmt.Fprintf(os.Stderr, "警告: 生成路径 %d 条，超过 --max-paths=%d 上限，已截断\n", len(paths), g.Opts.MaxPaths)
		paths = paths[:g.Opts.MaxPaths]
	}
	return paths
}

// expandTemplate 展开单个模板：多重占位符做笛卡尔积。
// 支持：{FUZZ} 主词字典；{ext} 扩展名；{HOST} 主机名；{PRODUCT} 产品名；
//
//	{range:1-100} 数字区间；{2digit}/{3digit} 定宽数字。
func (g *PathGen) expandTemplate(tpl string) []string {
	results := []string{tpl}

	fuzzVals := append([]string{}, g.Dict.Words...)
	fuzzVals = append(fuzzVals, g.Opts.Keywords...)
	fuzzVals = unique(fuzzVals)

	// 按顺序展开各类占位符，天然构成笛卡尔积
	results = expandPlaceholder(results, "{FUZZ}", fuzzVals)
	results = expandPlaceholder(results, "{ext}", g.Dict.Extensions)
	results = expandPlaceholder(results, "{PRODUCT}", g.Opts.Products)
	results = expandRanges(results)
	results = expandDigits(results)
	results = replaceAllLiteral(results, "{HOST}", g.Opts.Host)

	return results
}

// expandPlaceholder 将列表里所有含 ph 的串替换为每个候选值
func expandPlaceholder(list []string, ph string, values []string) []string {
	if len(values) == 0 {
		return list
	}
	var out []string
	for _, s := range list {
		if !strings.Contains(s, ph) {
			out = append(out, s)
			continue
		}
		for _, v := range values {
			out = append(out, strings.ReplaceAll(s, ph, v))
		}
	}
	return out
}

var rangeRe = regexp.MustCompile(`\{range:(\d+)-(\d+)\}`)

// expandRanges 展开 {range:1-100}
func expandRanges(list []string) []string {
	var out []string
	for _, s := range list {
		m := rangeRe.FindStringSubmatch(s)
		if m == nil {
			out = append(out, s)
			continue
		}
		lo, _ := strconv.Atoi(m[1])
		hi, _ := strconv.Atoi(m[2])
		if hi < lo || hi-lo > 100000 {
			out = append(out, s) // 防御：区间过大直接保留原样
			continue
		}
		for i := lo; i <= hi; i++ {
			out = append(out, rangeRe.ReplaceAllString(s, strconv.Itoa(i)))
		}
	}
	return out
}

var digitRe = regexp.MustCompile(`\{(\d+)digit\}`)

// expandDigits 展开 {2digit} → 00..99（前导零）
func expandDigits(list []string) []string {
	var out []string
	for _, s := range list {
		m := digitRe.FindStringSubmatch(s)
		if m == nil {
			out = append(out, s)
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if n <= 0 || n > 6 {
			out = append(out, s)
			continue
		}
		limit := 1
		for i := 0; i < n; i++ {
			limit *= 10
		}
		for i := 0; i < limit; i++ {
			val := fmt.Sprintf("%0*d", n, i)
			out = append(out, digitRe.ReplaceAllString(s, val))
		}
	}
	return out
}

func replaceAllLiteral(list []string, old, new string) []string {
	var out []string
	for _, s := range list {
		out = append(out, strings.ReplaceAll(s, old, new))
	}
	return out
}
