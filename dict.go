package main

import (
	"bufio"
	"embed"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

//go:embed dicts/*.txt
var dictFS embed.FS

//go:embed dicts/modules/*.txt
var moduleFS embed.FS

// Module 一个可装配字典模块（分层：通用性 layer；价值密度 density）
//
//	layer:   core / common / tech / sensitive
//	density: high / mid / low
type Module struct {
	Name    string   // 模块名（文件名去 .txt；内置件名 common/admin/extensions/backup）
	Layer   string   // 通用性分层
	Density string   // 价值密度
	Match   []string // 指纹关键词（--assemble 时命中任一即装配 tech 模块）
	Words   []string // 路径词（每行一个）
	IsExt   bool     // 词为扩展名列表（如 extensions.txt）
	IsSfx   bool     // 词为后缀组合词（如 backup.txt）
}

// builtinSpecs 内置四件套（对应旧版行为）
var builtinSpecs = []struct {
	name, file, layer, density string
	isExt, isSfx               bool
}{
	{"common", "common.txt", "core", "low", false, false},
	{"extensions", "extensions.txt", "core", "low", true, false},
	{"admin", "admin.txt", "sensitive", "high", false, false},
	{"backup", "backup.txt", "sensitive", "high", false, true},
}

// DefaultLayers 默认装配分层：core + sensitive == 旧版内置行为
var DefaultLayers = []string{"core", "sensitive"}

// assemblyOptions 装配选项
type assemblyOptions struct {
	layers    []string // 空 = DefaultLayers
	techForce bool     // --layers 含 tech 时强制装配全部 tech 模块
}

// ParseLayers 解析 --layers "core,tech" 到装配选项
func (o *assemblyOptions) wantLayers() []string {
	if len(o.layers) == 0 {
		return DefaultLayers
	}
	return o.layers
}

// Dictionary 字典集合：主词 + 扩展名 + 前缀/后缀 + 模块记录
type Dictionary struct {
	Words      []string
	Extensions []string
	Prefixes   []string
	Suffixes   []string
	Modules    []*Module // 已装配模块（报告/展示用）
}

// Clone 深拷贝字典（并行目标各自装配，避免共享底层数组）
func (d *Dictionary) Clone() *Dictionary {
	c := &Dictionary{
		Words:      append([]string{}, d.Words...),
		Extensions: append([]string{}, d.Extensions...),
		Prefixes:   append([]string{}, d.Prefixes...),
		Suffixes:   append([]string{}, d.Suffixes...),
	}
	for _, m := range d.Modules {
		mc := *m
		mc.Words = append([]string{}, m.Words...)
		mc.Match = append([]string{}, m.Match...)
		c.Modules = append(c.Modules, &mc)
	}
	return c
}

// 字典文件加载 -------------------------------------------------------------

func loadLinesFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "\uFEFF")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func loadEmbedded(name string) []string {
	data, err := dictFS.ReadFile("dicts/" + name)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "\uFEFF")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// tech 模块注册表进程内缓存：内嵌文件内容固定，解析一次即可，
// 避免 --assemble 多目标 / --layers tech 时重复解析上百个模块文件
var (
	techModulesOnce  sync.Once
	techModulesCache []*Module
)

// loadTechModules 返回内嵌 dicts/modules/*.txt 解析出的模块注册表（缓存）。
// 返回值共享，调用方不得修改模块对象；需要改动（如 AssembleTech 写入命中
// 关键词）必须先克隆。
func loadTechModules() []*Module {
	techModulesOnce.Do(func() {
		techModulesCache = loadTechModulesFS()
	})
	return techModulesCache
}

// loadTechModulesFS 扫描内嵌 dicts/modules/*.txt 作为 tech 模块注册表
func loadTechModulesFS() []*Module {
	entries, err := moduleFS.ReadDir("dicts/modules")
	if err != nil {
		return nil
	}
	var mods []*Module
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		data, rerr := moduleFS.ReadFile("dicts/modules/" + e.Name())
		if rerr != nil {
			continue
		}
		raw := strings.Split(string(data), "\n")
		var lines []string
		for _, ln := range raw {
			lines = append(lines, ln)
		}
		name, layer, density, match := parseModuleMeta(lines)
		if strings.TrimSpace(name) == "" {
			name = strings.TrimSuffix(e.Name(), ".txt")
		}
		var words []string
		for _, ln := range lines {
			l := strings.TrimSpace(strings.TrimPrefix(ln, "\uFEFF")) // 去 BOM
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			if strings.HasPrefix(l, "/") {
				l = strings.Trim(l, "/")
			}
			words = append(words, l)
		}
		mods = append(mods, &Module{Name: name, Layer: layer, Density: density, Match: match, Words: words})
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Name < mods[j].Name })
	return mods
}

// moduleRegistry 所有模块（内置 + tech），供 --list-modules
func moduleRegistry() []*Module {
	var mods []*Module
	for _, s := range builtinSpecs {
		words := loadEmbedded(s.file)
		if s.name == "admin" {
			var w []string
			for _, p := range words {
				p = strings.Trim(p, "/")
				if p != "" {
					w = append(w, p)
				}
			}
			words = w
		}
		mods = append(mods, &Module{Name: s.name, Layer: s.layer, Density: s.density, IsExt: s.isExt, IsSfx: s.isSfx, Words: words})
	}
	mods = append(mods, loadTechModules()...)
	return mods
}

// parseModuleMeta 解析模块文件头元数据
func parseModuleMeta(lines []string) (name, layer, density string, match []string) {
	layer, density = "tech", "mid" // 无元数据时的默认
	for _, ln := range lines {
		l := strings.TrimSpace(strings.TrimPrefix(ln, "\uFEFF"))
		if strings.HasPrefix(l, "# module:") {
			name = strings.TrimSpace(strings.TrimPrefix(l, "# module:"))
			continue
		}
		if !strings.HasPrefix(l, "# meta:") {
			continue
		}
		for _, kv := range strings.Fields(strings.TrimSpace(strings.TrimPrefix(l, "# meta:"))) {
			i := strings.Index(kv, "=")
			if i <= 0 {
				continue
			}
			k := strings.ToLower(strings.TrimSpace(kv[:i]))
			v := strings.TrimSpace(kv[i+1:])
			switch k {
			case "layer":
				layer = strings.ToLower(v)
			case "density":
				density = strings.ToLower(v)
			case "match":
				for _, mm := range strings.Split(v, ",") {
					mm = strings.TrimSpace(mm)
					if mm != "" {
						match = append(match, mm)
					}
				}
			}
		}
	}
	return name, layer, density, match
}

// BuildDictionary 构建基础字典（兼容旧入口）：
// 内置件按装配选项装载，-w 追加，--ext 覆盖扩展名。
// noBuiltin=true 时不加载任何内置件（仅 -w + --ext）。
func BuildDictionary(wordlists, extArg string, noBuiltin bool) (*Dictionary, error) {
	return BuildDictionaryAsm(wordlists, extArg, noBuiltin, assemblyOptions{})
}

// BuildDictionaryAsm 构建基础字典（分层装配版）：
//   - 内置件按 layers 装载（默认 core+sensitive；--density 不影响内置件，
//     密度过滤仅作用于 tech 模块装配，见 AssembleTech）；
//   - tech 模块不进入基础字典（需指纹装配，见 AssembleByFingerprint）；
//   - -w 外部词始终追加；--ext 自定义扩展名（非空时覆盖内置扩展名）。
func BuildDictionaryAsm(wordlists, extArg string, noBuiltin bool, o assemblyOptions) (*Dictionary, error) {
	d := &Dictionary{}
	if !noBuiltin {
		wantLayer := map[string]bool{}
		for _, l := range o.wantLayers() {
			wantLayer[l] = true
		}
		for _, s := range builtinSpecs {
			if !wantLayer[s.layer] {
				continue
			}
			words := loadEmbedded(s.file)
			if s.name == "admin" {
				var w []string
				for _, p := range words {
					p = strings.Trim(p, "/")
					if p != "" {
						w = append(w, p)
					}
				}
				words = w
			}
			m := &Module{Name: s.name, Layer: s.layer, Density: s.density, IsExt: s.isExt, IsSfx: s.isSfx, Words: words}
			d.addModuleWords(m)
			d.Modules = append(d.Modules, m)
		}
	}

	// 外部字典
	for _, wl := range strings.Split(wordlists, ",") {
		wl = strings.TrimSpace(wl)
		if wl == "" {
			continue
		}
		lines, err := loadLinesFile(wl)
		if err != nil {
			return nil, fmt.Errorf("加载字典 %s 失败: %w", wl, err)
		}
		d.Words = append(d.Words, lines...)
	}

	// 自定义扩展名覆盖内置
	if ext := strings.TrimSpace(extArg); ext != "" {
		parts := strings.Split(ext, ",")
		exts := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if !strings.HasPrefix(p, ".") && p != "~" {
				p = "." + p
			}
			exts = append(exts, p)
		}
		if len(exts) > 0 {
			d.Extensions = exts
		}
	}

	d.Words = unique(d.Words)
	d.Extensions = unique(d.Extensions)
	d.Suffixes = unique(d.Suffixes)
	return d, nil
}

// addModuleWords 把模块词按类型并入字典
func (d *Dictionary) addModuleWords(m *Module) {
	switch {
	case m.IsExt:
		d.Extensions = append(d.Extensions, m.Words...)
	case m.IsSfx:
		d.Suffixes = append(d.Suffixes, m.Words...)
	default:
		d.Words = append(d.Words, m.Words...)
	}
}

// AssembleTechModules 在字典副本上装配 tech 模块：
//   - force=true：装配全部 tech（--layers tech 强制）
//   - fp!=nil：只装配 Match 命中指纹的 tech 模块
//   - density：非空时只装配指定价值密度（high/mid/low）
// 返回 模块名 → 命中关键词列表 map（供 --assemble 展示匹配依据）
func (d *Dictionary) AssembleTech(force bool, fp *Fingerprint, density ...string) map[string][]string {
	wantDensity := map[string]bool{}
	for _, dd := range density {
		wantDensity[dd] = true
	}
	picked := map[string][]string{}
	for _, m := range loadTechModules() {
		if len(wantDensity) > 0 && !wantDensity[m.Density] {
			continue
		}
		if !force && fp == nil {
			continue
		}
		mc := *m // 注册表已缓存共享：克隆后再写命中关键词，避免污染其他目标的装配
		if fp != nil {
			var hit []string
			for _, k := range m.Match {
				if fp.Matches(k) {
					hit = append(hit, k)
				}
			}
			if len(hit) == 0 && !force {
				continue
			}
			mc.Match = hit // 保留命中的关键词，供展示与报告
		}
		d.addModuleWords(&mc)
		d.Modules = append(d.Modules, &mc)
		picked[m.Name] = mc.Match
	}
	d.Words = unique(d.Words)
	d.Extensions = unique(d.Extensions)
	d.Suffixes = unique(d.Suffixes)
	return picked
}

// LoadPrefixSuffix 加载前缀/后缀组合字典
func (d *Dictionary) LoadPrefixSuffix(prefixPath, suffixPath string) error {
	if prefixPath != "" {
		lines, err := loadLinesFile(prefixPath)
		if err != nil {
			return fmt.Errorf("加载前缀字典 %s 失败: %w", prefixPath, err)
		}
		d.Prefixes = unique(lines)
	}
	if suffixPath != "" {
		lines, err := loadLinesFile(suffixPath)
		if err != nil {
			return fmt.Errorf("加载后缀字典 %s 失败: %w", suffixPath, err)
		}
		d.Suffixes = unique(lines)
	}
	return nil
}

// unique 去重（保持顺序）
func unique(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// 关键词生成器 -------------------------------------------------------------

// genKeywords 根据目标域名与产品名自动生成针对性字典
func genKeywords(domain string, products []string) []string {
	host := normalizeHost(domain)
	base := host
	if i := strings.Index(base, "."); i > 0 {
		base = base[:i]
	}
	set := map[string]struct{}{}
	add := func(s string) {
		if s != "" {
			set[s] = struct{}{}
		}
	}
	add(base)
	add(host)
	if !strings.HasPrefix(host, "www.") {
		add("www." + host)
	}
	seps := []string{"", "-", "_", "."}
	suffixWords := []string{
		"backup", "bak", "old", "dev", "staging", "test",
		"temp", "tmp", "new", "copy", "orig", "save",
		"admin", "console", "panel", "manage", "control",
		"2024", "2025", "2026",
	}
	prefixWords := []string{"backup", "bak", "old", "dev", "test", "tmp", "new", "copy"}
	for _, sep := range seps {
		for _, sfx := range suffixWords {
			add(base + sep + sfx)
			add(host + sep + sfx)
		}
		for _, pfx := range prefixWords {
			add(pfx + sep + base)
		}
	}
	for _, p := range products {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		add(p)
		for _, sep := range seps {
			add(p + sep + "backup")
			add(p + sep + "config")
			add(p + sep + "admin")
			add(p + sep + "test")
		}
	}
	return unique(mapKeys(set))
}

// normalizeHost 从目标串提取主机名
func normalizeHost(target string) string {
	host := strings.TrimSpace(target)
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	if i := strings.IndexAny(host, "/\\"); i >= 0 {
		host = host[:i]
	}
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimPrefix(host, "www.")
	return strings.ToLower(host)
}

func mapKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// printModuleRegistry 打印全部字典模块（内置 + tech）清单：层/密度/词数/指纹关键词
func printModuleRegistry(densityFilter string) {
	dens := map[string]bool{}
	for _, d := range strings.Split(densityFilter, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			dens[d] = true
		}
	}
	mods := moduleRegistry()
	var total int
	fmt.Println("MuLuSaoMiao 字典模块注册表")
	fmt.Printf("%-16s %-10s %-8s %7s  %s\n", "模块", "层(layer)", "密度", "词数", "指纹关键词(match)")
	for _, m := range mods {
		if len(dens) > 0 && !dens[m.Density] {
			continue
		}
		n := len(m.Words)
		total += n
		kind := ""
		switch {
		case m.IsExt:
			kind = "[扩展名]"
		case m.IsSfx:
			kind = "[后缀]"
		}
		name := kind + m.Name
		mt := strings.Join(m.Match, ",")
		if mt == "" {
			mt = "-"
		}
		fmt.Printf("%-16s %-10s %-8s %7d  %s\n", name, m.Layer, m.Density, n, mt)
	}
	fmt.Printf("共 %d 个模块，%d 词条\n", len(mods), total)
}