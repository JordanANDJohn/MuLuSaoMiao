package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const version = "1.0.0"

type options struct {
	target      string
	targetFile  string
	wordlists   string
	noBuiltin   bool
	prefixDict  string
	suffixDict  string
	combine     string
	smartExt    bool
	extList     string
	keyword     string
	fuzz        string
	concurrency int
	timeout     int
	method      string
	show        string
	hide        string
	follow      bool
	randomUA    bool
	cookie      string
	output      []string
	noColor     bool
	quiet       bool
	version     bool

	// 高性能并发与速率控制
	rate       float64
	burst      int
	jitter     int
	maxRetries int
	maxConns   int
	auto       bool
	autoMax    int

	// 智能响应分析
	ac      bool
	acCount int

	// 重定向智能处理
	fakeRedir bool

	// 请求定制与身份模拟
	headers     stringSlice
	ua          string
	body        string
	contentType string
	proxy       string

	// 被动情报收集
	extract  bool
	crawl    bool
	crawlMax int

	// 递归目录扫描
	depth    int
	maxDirs  int

	// 多目标并发
	parallel int

	// 分层模块化字典 + 指纹装配
	assemble  bool   // --assemble：指纹自动装配 tech 模块
	fpOnly    bool   // --fingerprint：仅识别指纹并输出，不扫描
	layers    string // --layers：分层装配 core,common,tech,sensitive
	density   string // --density：价值密度过滤 high,mid,low（仅作用于 tech 模块）
	listDicts bool   // --list-dicts：列出全部模块后退出

	// 路径护栏与传输安全
	maxPaths  int  // --max-paths：单目标最大生成路径数（0 = 不限制）
	tlsVerify bool // --tls-verify：校验服务器 TLS 证书（默认跳过）
}

// stringSlice 支持重复 -H/--header 的 flag 值
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// collectOutputs 拆分 -o 列表：逗号分隔 + 去空项（支持 -o a.json,b.html 或多次 -o）
func collectOutputs(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// containsPath 判断切片中是否已含指定路径
func containsPath(list []string, p string) bool {
	for _, v := range list {
		if v == p {
			return true
		}
	}
	return false
}

// countModuleWords 统计已装配模块（按名字 map 键）新增的词条数
func countModuleWords(picked map[string][]string, d *Dictionary) int {
	var n int
	for _, m := range d.Modules {
		if _, ok := picked[m.Name]; ok {
			n += len(m.Words)
		}
	}
	return n
}

// formatPicked 格式化装配结果：module[关键词1,关键词2]
func formatPicked(picked map[string][]string) string {
	names := sortedMapKeys(picked)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		if len(picked[n]) > 0 {
			parts = append(parts, n+"["+strings.Join(picked[n], ",")+"]")
		} else {
			parts = append(parts, n)
		}
	}
	return strings.Join(parts, " ")
}

// formatPickedShort 只输出模块名（逗号分隔）
func formatPickedShort(picked map[string][]string) string {
	return strings.Join(sortedMapKeys(picked), ",")
}

// sortedMapKeys 对 map 键排序
func sortedMapKeys(m map[string][]string) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// isValidMethod 校验 HTTP 方法 token：标准方法白名单 + 任意合法 token（大写字母与部分符号）
func isValidMethod(m string) bool {
	switch m {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		return true
	}
	// 自定义方法：允许大写字母、数字和 -_. 等 token 字符
	if m == "" {
		return false
	}
	for _, r := range m {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// parseHeaders 解析 -H/--header 列表 → 请求头 map。
// 每条支持 "Name: Value"；逗号可分隔多个（值内不含逗号时）。
func parseHeaders(list []string) map[string]string {
	out := make(map[string]string)
	for _, item := range list {
		for _, part := range strings.Split(item, ",") {
			i := strings.Index(part, ":")
			if i <= 0 {
				continue
			}
			name := strings.TrimSpace(part[:i])
			val := strings.TrimSpace(part[i+1:])
			if name != "" {
				out[name] = val
			}
		}
	}
	return out
}

// loadBody 解析 --body：普通文本原样；@文件路径则读文件
func loadBody(spec string) []byte {
	if spec == "" {
		return nil
	}
	if strings.HasPrefix(spec, "@") {
		b, err := os.ReadFile(strings.TrimPrefix(spec, "@"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "读取请求体文件失败: %v\n", err)
			os.Exit(1)
		}
		return b
	}
	return []byte(spec)
}

// parseProxy 解析 --proxy：自动补 http://；支持 http/https/socks5 协议
func parseProxy(v string) *url.URL {
	if v == "" {
		return nil
	}
	raw := v
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "代理地址无效: %v\n", err)
		os.Exit(1)
	}
	return u
}

func main() {
	var o options
	flag.StringVar(&o.target, "u", "", "目标 URL，如 http://example.com/（无协议自动补 http://；可配合 -f 批量）")
	flag.StringVar(&o.targetFile, "f", "", "目标文件，每行一个 URL（# 注释）")
	flag.IntVar(&o.parallel, "parallel", 0, "多目标并发数（0=自动 min(4,目标数)；1=串行；>1=同时扫 N 个目标）")
	flag.StringVar(&o.wordlists, "w", "", "字典文件（可多次 -w，或逗号分隔多个）")
	flag.BoolVar(&o.noBuiltin, "no-builtin", false, "不使用内置字典，仅用 -w 指定的自建字典（--ext 扩展名仍有效）")
	flag.StringVar(&o.prefixDict, "prefix-dict", "", "前缀字典文件（配合 --combine）")
	flag.StringVar(&o.suffixDict, "suffix-dict", "", "后缀字典文件（配合 --combine）")
	flag.StringVar(&o.combine, "combine", "none", "多字典组合：none|prefix|suffix|both")
	flag.BoolVar(&o.smartExt, "smart-ext", false, "智能追加扩展名：.bak/.old/.tar.gz/~/…")
	flag.StringVar(&o.extList, "ext", "", "自定义扩展名列表（逗号分隔，覆盖内置；如 bak,old,~）")
	flag.StringVar(&o.keyword, "keyword", "", "关键词（域名/产品名，逗号分隔），自动生成针对性变体")
	flag.StringVar(&o.fuzz, "fuzz", "", "模糊测试模板，如 /api/{FUZZ}/v1（可逗号分隔多个）")
	flag.IntVar(&o.maxPaths, "max-paths", 0, "单目标最大生成路径数（0 = 不限制），超过时截断并警告")
	flag.IntVar(&o.concurrency, "c", 50, "并发数")
	flag.IntVar(&o.timeout, "timeout", 5, "请求超时（秒）")
	flag.StringVar(&o.method, "m", "GET", "HTTP 方法：GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS 等")
	flag.Var(&o.headers, "H", "自定义请求头（可多次），如 -H \"Authorization: Bearer x\" -H \"X-Forwarded-For: 1.2.3.4\"")
	flag.Var(&o.headers, "header", "自定义请求头（同 -H）")
	flag.StringVar(&o.ua, "ua", "", "User-Agent：browser/bot/random 或完整 UA 字符串")
	flag.StringVar(&o.body, "body", "", "请求体：文本或 @文件路径（如 @post.json，配合 -m POST 测试 API）")
	flag.StringVar(&o.contentType, "content-type", "", "请求体 Content-Type（默认 application/x-www-form-urlencoded）")
	flag.StringVar(&o.proxy, "proxy", "", "代理地址：http://127.0.0.1:8080 或 socks5://127.0.0.1:1080（联动 Burp 等）")
	flag.BoolVar(&o.tlsVerify, "tls-verify", false, "校验服务器 TLS 证书（默认关闭：跳过自签名/过期证书）")
	flag.BoolVar(&o.extract, "extract", false, "被动提取：从响应页面链接/HTML注释/JS/robots.txt/sitemap 提取新路径并自动扫描")
	flag.BoolVar(&o.extract, "passive", false, "被动提取（同 --extract）")
	flag.BoolVar(&o.crawl, "crawl", false, "爬行模式：被动提取 + 以目标首页为起点（无需字典也能爬）")
	flag.IntVar(&o.crawlMax, "crawl-max", 500, "每目标被动提取路径上限")
	flag.IntVar(&o.depth, "depth", 1, "递归扫描深度：1=仅初始目录；2=子目录再扫一层…；0=不限深度（受 --max-dirs 保护）")
	flag.IntVar(&o.maxDirs, "max-dirs", 20, "递归时每层最多进入的子目录数（0=不限）")
	flag.BoolVar(&o.assemble, "assemble", false, "指纹自动装配：先识别目标技术栈指纹，再装配匹配的 tech 字典模块")
	flag.BoolVar(&o.fpOnly, "fingerprint", false, "仅识别目标指纹并输出（不扫描）；供侦察/自动装配参考")
	flag.StringVar(&o.layers, "layers", "", "分层装配字典：core,common,tech,sensitive（逗号分隔；默认 core,sensitive；含 tech 时强制装配全部 tech 模块）")
	flag.StringVar(&o.density, "density", "", "价值密度过滤：high,mid,low（逗号分隔；默认全部；仅作用于 tech 模块装配，不影响内置层）")
	flag.BoolVar(&o.listDicts, "list-dicts", false, "列出全部内置字典模块（层/密度/词数/指纹关键词）后退出")
	flag.StringVar(&o.show, "status", "", "只显示这些状态码（白名单，优先于 --hide；支持 2xx/4xx/40x 通配）")
	flag.StringVar(&o.hide, "hide", "", "隐藏这些状态码（逗号分隔，如 404）")
	flag.BoolVar(&o.follow, "follow", false, "跟随重定向（默认只记录 Location）")
	flag.BoolVar(&o.randomUA, "random-agent", false, "随机 User-Agent")
	flag.StringVar(&o.cookie, "cookie", "", "请求 Cookie，如 session=abc123")
	flag.Func("o", "输出文件：可多次 -o 或逗号分隔多个（按各文件扩展名 .json/.csv/.html/.md/.xlsx/.docx/其他→txt）", func(v string) error {
		for _, p := range collectOutputs(v) {
			o.output = append(o.output, p)
		}
		return nil
	})
	flag.BoolVar(&o.noColor, "no-color", false, "禁用彩色输出")
	flag.BoolVar(&o.quiet, "quiet", false, "安静模式（不显示进度条）")
	flag.BoolVar(&o.version, "version", false, "显示版本号")
	flag.Float64Var(&o.rate, "rate", 0, "每秒最大请求数（0 = 不限速）")
	flag.IntVar(&o.burst, "burst", 0, "令牌桶突发容量（默认 = rate）")
	flag.IntVar(&o.jitter, "jitter", 0, "请求间隔抖动百分比 0-100（模拟真实流量）")
	flag.IntVar(&o.maxRetries, "max-retries", 0, "网络失败重试次数（指数退避）")
	flag.IntVar(&o.maxConns, "max-conns", 0, "单目标最大连接数（0 = 不限制）")
	flag.BoolVar(&o.auto, "auto", false, "自适应并发：按延迟/错误率动态调整")
	flag.IntVar(&o.autoMax, "auto-max", 0, "自适应并发上限（默认 = 4×-c）")
	flag.BoolVar(&o.ac, "ac", false, "智能响应分析：先请求随机路径学习无效页特征，自动过滤")
	flag.IntVar(&o.acCount, "ac-count", 5, "校准用随机路径数量（默认 5）")
	flag.BoolVar(&o.fakeRedir, "fake-redir", false, "识别并过滤跳转到首页/登录页的伪有效目录")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `MuLuSaoMiao v%s — Go 目录扫描工具（字典管理 + 路径生成 + 模糊测试）

用法:
  MuLuSaoMiao -u http://example.com
  MuLuSaoMiao -u http://example.com -w extra.txt --smart-ext
  MuLuSaoMiao -u http://example.com --combine both -prefix-dict p.txt -suffix-dict s.txt
  MuLuSaoMiao -u http://example.com --keyword example.com,wordpress --smart-ext
  MuLuSaoMiao -u http://example.com -fuzz '/api/{FUZZ}', '/v1/{FUZZ}/{ext}'
  MuLuSaoMiao -u http://example.com -o report.json,report.html,report.xlsx   # 一次扫描，多格式报告

参数:
`, version)
		printHelpOrdered()
	}
	flag.Parse()

	if o.version {
		fmt.Println("MuLuSaoMiao", version)
		return
	}

	// 列出全部字典模块后退出（无需目标）
	if o.listDicts {
		printModuleRegistry(o.density)
		return
	}

	if o.target == "" && o.targetFile == "" {
		flag.Usage()
		os.Exit(1)
	}
	o.method = strings.ToUpper(o.method)
	if !isValidMethod(o.method) {
		fmt.Fprintf(os.Stderr, "错误: -m 方法无效 %q（支持 GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS 等标准方法或自定义大写 token）\n", o.method)
		os.Exit(1)
	}
	switch o.combine {
	case "none", "prefix", "suffix", "both":
	default:
		fmt.Fprintln(os.Stderr, "错误: --combine 只支持 none|prefix|suffix|both")
		os.Exit(1)
	}

	// 分层/密度参数校验
	layers := splitCSV(o.layers)
	for _, l := range layers {
		switch l {
		case "core", "common", "tech", "sensitive":
		default:
			fmt.Fprintf(os.Stderr, "错误: --layers 含无效层 %q（支持 core|common|tech|sensitive）\n", l)
			os.Exit(1)
		}
	}
	densities := splitCSV(o.density)
	for _, d := range densities {
		switch d {
		case "high", "mid", "low":
		default:
			fmt.Fprintf(os.Stderr, "错误: --density 含无效值 %q（支持 high|mid|low）\n", d)
			os.Exit(1)
		}
	}
	if o.assemble && o.noBuiltin {
		fmt.Fprintln(os.Stderr, "警告: --assemble 与 --no-builtin 同时使用，tech 模块仍会装配（仅跳过内置件）")
	}
	// 参数组合提示（不影响运行）
	if strings.TrimSpace(o.show) != "" && strings.TrimSpace(o.hide) != "" {
		fmt.Fprintln(os.Stderr, "警告: 同时指定 --status 与 --hide，白名单优先，--hide 将被忽略")
	}
	if strings.TrimSpace(o.extList) != "" && !o.smartExt && !strings.Contains(o.fuzz, "{ext}") {
		fmt.Fprintln(os.Stderr, "提示: --ext 仅在 --smart-ext 或 -fuzz 模板含 {ext} 时生效，当前配置不会追加扩展名")
	}

	// 字典（分层装配：默认 core+sensitive；tech 模块按指纹/层装配；
	// --density 仅作用于 tech 模块装配，内置 core/sensitive 层不受影响）
	ym := assemblyOptions{layers: layers}
	dict, err := BuildDictionaryAsm(o.wordlists, o.extList, o.noBuiltin, ym)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// --layers 含 tech：强制装配全部 tech 模块到共享字典
	// （--assemble 的指纹装配在每个目标分别执行，见下）
	techForce := false
	for _, l := range layers {
		if l == "tech" {
			techForce = true
		}
	}
	if techForce {
		picked := dict.AssembleTech(true, nil, densities...)
		if !o.quiet {
			fmt.Fprintf(os.Stderr, "装配 tech 模块（--layers 强制）：%s（新增 %d 条）\n",
				formatPickedShort(picked), countModuleWords(picked, dict))
		}
	}
	if o.noBuiltin && len(dict.Words) == 0 {
		fmt.Fprintln(os.Stderr, "错误: --no-builtin 需要配合 -w 提供自定义字典")
		os.Exit(1)
	}
	if err := dict.LoadPrefixSuffix(o.prefixDict, o.suffixDict); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// 目标列表
	targets, err := loadTargets(o.target, o.targetFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	keywords := splitCSV(o.keyword)
	products := keywords // -keyword 即作为关键词也作为产品名

	var fuzzTpls []string
	if o.fuzz != "" {
		fuzzTpls = splitCSV(o.fuzz)
	}

	// Ctrl+C 中断：第一次置位标志取消在途请求、尽快停止；第二次强制退出
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := &atomic.Bool{}
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "\n收到中断，正在停止…（再次 Ctrl+C 立即退出）")
		cancel()        // 立即终止所有在途 HTTP 请求（context 传播）
		stop.Store(true)
		<-sig
		fmt.Fprintln(os.Stderr, "已强制退出")
		os.Exit(130)
	}()

	noColor := o.noColor || !stdoutIsTTY()

	uaMode, customUA := ParseUAMode(o.ua)
	if o.randomUA {
		uaMode = UAModeRandom
	}

	scanner := &Scanner{
		Method:      o.method,
		Concurrency: o.concurrency,
		Timeout:     time.Duration(o.timeout) * time.Second,
		Follow:      o.follow,
		RandomUA:    o.randomUA,
		Cookie:      o.cookie,
		Show:        parsedStatusSet(o.show),
		Hide:        parsedStatusSet(o.hide),
		Quiet:       o.quiet || noColor,
		Stop:        stop,
		Ctx:         ctx, // Ctrl+C 时取消所有在途请求
		Rate:        NewRateLimiter(o.rate, o.burst, float64(o.jitter)/100),
		MaxConns:    o.maxConns,
		MaxRetries:  o.maxRetries,
		Auto:        o.auto,
		AutoMax:     o.autoMax,
		Headers:     parseHeaders(o.headers),
		Body:        loadBody(o.body),
		ContentType: o.contentType,
		ProxyURL:    parseProxy(o.proxy),
		UAMode:      uaMode,
		CustomUA:    customUA,
		TlsVerify:   o.tlsVerify,
		Extract:     o.extract || o.crawl,
		ExtractMax:  o.crawlMax,
		Discovered:  &atomic.Int64{},
		DroppedFake: &atomic.Int64{},
	}

	totalPaths := 0
	var all []Result
	started := time.Now()

	// 多目标并发：0 = 自动 min(4, 目标数)；1 = 串行（保持历史行为）；>1 = 同时扫 N 个目标。
	// 注意：每个目标内部仍按 -c 并发；目标并发 × -c 为实际总并发，勿在共享网络上开得过大。
	parallel := o.parallel
	if parallel <= 0 {
		parallel = 4
	}
	if parallel > len(targets) {
		parallel = len(targets)
	}
	if parallel < 1 {
		parallel = 1
	}
	if o.ac && o.acCount < 1 {
		o.acCount = 1
	}

	// 多目标并行时共享一条进度条（各 Scanner 副本共用同一 SharedProgress，
	// 避免多个 "\r" 互相覆盖）；串行/单目标不启用，保持原有行为。
	var sharedProg *SharedProgress
	if parallel > 1 {
		sharedProg = &SharedProgress{}
	}

	var (
		allMu     sync.Mutex // 保护 all / totalPaths
		outMu     sync.Mutex // 保护 stderr 终端输出（校准/提示/渲染，防并行交错）
		discTotal atomic.Int64
		dropTotal atomic.Int64
	)
	startSem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for gIdx, target := range targets {
		if stop.Load() { // 校准期间也可被中断
			break
		}
		wg.Add(1)
		startSem <- struct{}{}
		go func(target string, gIdx int) {
			defer wg.Done()
			defer func() { <-startSem }()

			target = strings.TrimRight(target, "/")

			// 每个目标独立 Scanner 副本：仅共享不可变配置（Stop/Rate/Headers/…）
			// 与 Ctx；Baseline/FakeRedirect/计数每目标独立，避免并行时互相覆盖。
			sc := *scanner
			sc.Baseline = nil
			sc.ComputeHash = false
			sc.FakeRedirect = nil
			sc.Discovered = &atomic.Int64{}
			sc.DroppedFake = &atomic.Int64{}
			if sharedProg != nil {
				sc.Progress = sharedProg // 并行共享进度条
				sc.Quiet = o.quiet || noColor // 安静模式仍由用户控制（进度条开关）
			}
			// 会话级 UA：随机模式每目标固定一个，避免每请求切换 UA 形成明显爬虫特征
			sc.SessionUA = scanner.userAgent()

			// 每个目标独立随机源（LearnBaseline 并发使用不共享 rand.Rand）
			rng := rand.New(rand.NewSource(time.Now().UnixNano() ^ int64(gIdx)))

			// 智能响应分析：学习无效页基准（每个目标独立学习）
			if o.ac {
				sc.Baseline = LearnBaseline(&sc, target, o.acCount, rng)
				sc.ComputeHash = sc.Baseline != nil
				if sc.Baseline != nil && !o.quiet {
					outMu.Lock()
					fmt.Fprintf(os.Stderr, "校准 %s：%s\n", target, sc.Baseline.Summary())
					outMu.Unlock()
				}
			}

			// 重定向智能处理：学习首页/登录页指纹（每个目标独立学习）
			if o.fakeRedir {
				sc.FakeRedirect = LearnFakeRedirects(&sc, target)
				if sc.FakeRedirect != nil && !o.quiet {
					outMu.Lock()
					fmt.Fprintf(os.Stderr, "伪重定向指纹 %s：%s\n", target, sc.FakeRedirect.Summary())
					outMu.Unlock()
				}
			}

			// 指纹识别与自动装配：--assemble 时先识别目标技术栈，
			// 克隆共享字典并装配命中的 tech 模块；--fingerprint 仅侦察。
			useDict := dict
			var fp *Fingerprint
			if o.assemble || o.fpOnly {
				fp = InferFingerprint(&sc, target)
				if !o.quiet {
					outMu.Lock()
					fmt.Fprintf(os.Stderr, "指纹 %s：%s\n", target, fp.Summary())
					outMu.Unlock()
				}
				if o.assemble {
					useDict = dict.Clone()
					picked := useDict.AssembleTech(techForce, fp, densities...)
					if !o.quiet {
						outMu.Lock()
						fmt.Fprintf(os.Stderr, "装配 %s：%s（新增 %d 条）\n", target, formatPicked(picked), countModuleWords(picked, useDict))
						outMu.Unlock()
					}
				}
				if o.fpOnly {
					return // 仅指纹模式：不扫描
				}
			}

			// 关键词：正常模式自动按域名生成；--no-builtin 时仅保留显式 -keyword
			kw := products
			if !o.noBuiltin {
				kw = genKeywords(target, products)
			}
			g := &PathGen{
				Dict: useDict,
				Opts: GenOptions{
					Combine:  o.combine,
					SmartExt: o.smartExt,
					FuzzTpls: fuzzTpls,
					Keywords: kw,
					Products: products,
					Host:     normalizeHost(target),
					MaxPaths: o.maxPaths,
				},
			}
			paths := g.Generate()
			if sharedProg != nil {
				sharedProg.AddTotal(int64(len(paths)))
			}
			if o.crawl && !containsPath(paths, "/") {
				paths = append([]string{"/"}, paths...)
			}
			if !o.quiet {
				outMu.Lock()
				fmt.Fprintf(os.Stderr, "目标 %s：共生成 %d 条路径\n", target, len(paths))
				outMu.Unlock()
			}
			// totalPaths 只累加 ScanLevels 的计划请求数（已含首层）；
			// len(paths) 仅为首层生成数，再加一次统计与报告 total_paths 会翻倍。
			results, reqTotal := sc.ScanLevels(target, paths, o.depth, o.maxDirs)
			if len(targets) > 1 { // 多目标时标记归属，报告/JSON 可按目标筛选
				for i := range results {
					results[i].Target = target
				}
			}

			allMu.Lock()
			totalPaths += reqTotal
			all = append(all, results...)
			allMu.Unlock()
			discTotal.Add(sc.Discovered.Load())
			dropTotal.Add(sc.DroppedFake.Load())

			outMu.Lock()
			RenderConsole(results, noColor)
			outMu.Unlock()
		}(target, gIdx)
	}
	wg.Wait()

	if !o.quiet {
		extra := ""
		if discovered := discTotal.Load(); discovered > 0 {
			extra = fmt.Sprintf(" / 被动发现 %d 条", discovered)
		}
		if dropped := dropTotal.Load(); dropped > 0 {
			extra += fmt.Sprintf(" / 剔除伪重定向 %d 条", dropped)
		}
		fmt.Fprintf(os.Stderr, "\n扫描完成：%d 目标 / %d 路径 / %d 结果 / 耗时 %s%s\n",
			len(targets), totalPaths, len(all), time.Since(started).Round(time.Millisecond), extra)
	}

	if len(o.output) > 0 {
		all = sortedResults(all)
		reportTarget := o.target
		if reportTarget == "" {
			if len(targets) == 1 {
				reportTarget = targets[0]
			} else if len(targets) > 1 {
				reportTarget = fmt.Sprintf("%d 个目标", len(targets))
			}
		}
		for _, out := range o.output {
			if err := SaveReport(all, reportTarget, out, started, int64(totalPaths), o.method); err != nil {
				fmt.Fprintln(os.Stderr, "保存结果失败:", err)
				os.Exit(1)
			}
		}
	}
}

// loadTargets 组装目标列表（自动规范化并去重，等效 URL 只保留一条）。
// 归一化规则：
//   - 无协议目标自动补 http://（example.com、example.com:8080/path 等）；
//   - 去尾部空白；跳过空行与 # 注释；
//   - 去尾斜杠（/admin/ 与 /admin 视为同一目标）；
//   - 归一化默认端口（http:80 / https:443 的显式端口与被省略等价）；
//   - 主机名/路径大小写保持不变（语义上仍是同一站点）。
// 非 http(s) 或解析失败的行告警跳过；全部无效时报"没有有效目标"。
func loadTargets(single, file string) ([]string, error) {
	var targets []string
	var invalid []string
	seen := make(map[string]bool)
	add := func(line string) {
		norm := normalizeTarget(line)
		if norm == "" {
			return
		}
		// 只接受 http(s) 目标（normalizeTarget 已为无协议目标补 http://）
		if u, err := url.Parse(norm); err != nil || u.Host == "" ||
			(u.Scheme != "http" && u.Scheme != "https") {
			invalid = append(invalid, strings.TrimSpace(line))
			return
		}
		if seen[norm] {
			return
		}
		seen[norm] = true
		targets = append(targets, norm)
	}
	if single != "" {
		add(single)
	}
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			add(line)
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	for _, t := range invalid {
		fmt.Fprintf(os.Stderr, "警告: 跳过无效目标 %q（需 http/https URL）\n", t)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("没有有效目标")
	}
	return targets, nil
}

// normalizeTarget 归一化单个目标 URL：
//   - 无协议时自动补 http://（纯主机名/带端口/带路径均可）；
//   - 去尾斜杠；
//   - 显式默认端口（:80/:443）归一化；
//   - 解析失败时原样返回（有效性由 loadTargets 校验并告警）。
func normalizeTarget(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	s = strings.TrimRight(s, "/")
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return s
	}
	port := u.Port()
	def := ""
	switch strings.ToLower(u.Scheme) {
	case "http":
		def = "80"
	case "https":
		def = "443"
	}
	if port != "" && port == def {
		u.Host = u.Hostname()
	}
	return u.String()
}

// splitCSV 逗号分隔 → 去空字符串切片
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// stdoutIsTTY 检测 stdout 是否为终端（管道输出时禁用颜色）
func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// helpOrder 帮助参数的展示顺序：从上到下 = 常用 → 不常用。
// 未列出的参数（兜底）按字母序排在列表之后。
var helpOrder = []string{
	// 目标与输出
	"u", "f", "parallel", "o",
	// 字典与路径生成
	"w", "layers", "density", "assemble", "fingerprint", "list-dicts",
	"combine", "prefix-dict", "suffix-dict",
	"smart-ext", "ext", "keyword", "fuzz", "max-paths",
	// 扫描核心
	"c", "timeout", "m", "status", "hide",
	// 请求定制与身份模拟
	"H", "header", "cookie", "ua", "random-agent", "body", "content-type", "proxy", "tls-verify",
	// 重定向与智能分析
	"follow", "fake-redir", "ac", "ac-count",
	// 递归
	"depth", "max-dirs",
	// 限速与自适应
	"rate", "burst", "jitter", "auto", "auto-max", "max-retries", "max-conns",
	// 被动情报
	"extract", "passive", "crawl", "crawl-max",
	// 显示与杂项
	"no-color", "quiet", "version",
}

// printHelpOrdered 按 helpOrder 打印帮助参数（替代 flag.PrintDefaults 的字典序）。
func printHelpOrdered() {
	byName := make(map[string]*flag.Flag)
	flag.VisitAll(func(f *flag.Flag) {
		byName[f.Name] = f
	})
	shown := make(map[string]bool)
	for _, name := range helpOrder {
		if f, ok := byName[name]; ok {
			printHelpFlag(f)
			shown[name] = true
		}
	}
	// 未列入 helpOrder 的 flag 按字母序补在最后
	rest := make([]string, 0, len(byName))
	for _, f := range byName {
		if !shown[f.Name] {
			rest = append(rest, f.Name)
		}
	}
	sort.Strings(rest)
	for _, name := range rest {
		printHelpFlag(byName[name])
	}
}

// printHelpFlag 以 flag.PrintDefaults 风格打印单个参数行。
func printHelpFlag(f *flag.Flag) {
	typ, usage := flag.UnquoteUsage(f)
	if typ == "" {
		fmt.Fprintf(os.Stderr, "  -%s\n", f.Name)
	} else {
		fmt.Fprintf(os.Stderr, "  -%s %s\n", f.Name, typ)
	}
	fmt.Fprintf(os.Stderr, "    \t%s", usage)
	if d := f.DefValue; d != "" && d != "false" && d != "0" && d != "0.000000" && d != "[]" {
		fmt.Fprintf(os.Stderr, " (默认 %s)", d)
	}
	fmt.Fprintln(os.Stderr)
}
