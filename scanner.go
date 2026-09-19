package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Result 单条扫描结果
type Result struct {
	URL      string   `json:"url"`
	Target   string   `json:"target,omitempty"` // 所属目标（多目标 -f 时区分归属，单目标省略）
	Status   int      `json:"status"`
	Size     int64    `json:"size"`
	TimeMs   int64    `json:"time_ms"`
	Title    string   `json:"title,omitempty"`
	Location string   `json:"location,omitempty"` // 首个重定向 Location（未跟随模式）
	Server   string   `json:"server,omitempty"`
	Hash     string   `json:"hash,omitempty"`     // 响应体 MD5（--ac 时计算）
	Chain    []string `json:"chain,omitempty"`    // 重定向链（--follow 时，含每跳目标 URL）
	FinalURL string   `json:"final_url,omitempty"` // 重定向链最终落地 URL（--follow 时）

	Body        []byte `json:"-"` // 响应体（仅被动提取时保留，导出忽略）
	ContentType string `json:"-"` // 响应 Content-Type
}

// redirectChainKey 携带每个请求的重定向链收集容器
type redirectChainKeyType struct{}

var redirectChainKey redirectChainKeyType

// withRedirectChain 把链容器挂到请求上下文，CheckRedirect 回调向其中追加跳转目标
func withRedirectChain(ctx context.Context, chain *[]string) context.Context {
	return context.WithValue(ctx, redirectChainKey, chain)
}

// Scanner HTTP 扫描引擎配置
type Scanner struct {
	Method      string
	Concurrency int
	Timeout     time.Duration
	Follow      bool
	RandomUA    bool
	Cookie      string
	Show        map[int]bool // 状态码白名单；空 = 全部
	Hide        map[int]bool // 状态码黑名单
	Quiet       bool
	Progress    *SharedProgress // 多目标共享进度（nil = 单目标自身 \r 进度）
	Stop        *atomic.Bool // 中断标志（Ctrl+C）
	Ctx         context.Context // 请求取消上下文（Ctrl+C 置位；nil = 使用 Background）

	// 高性能并发与速率控制
	Rate       *RateLimiter // 全局限速（nil = 不限速）
	MaxConns   int          // 单目标最大连接数（0 = 不限制）
	MaxRetries int          // 网络失败重试次数
	Auto       bool         // 自适应并发（按延迟/错误率动态调整）
	AutoMax    int          // 自适应并发上限（0 = 默认 4×初始并发）

	// 智能响应分析
	Baseline    *Baseline // 无效页面基准（nil = 关闭）：命中该特征的响应视为无效
	ComputeHash bool      // 计算响应体 MD5（供基准哈希匹配）

	// 重定向智能处理
	FakeRedirect *FakeRedirectFilter // 伪有效目录过滤器（nil = 关闭）
	DroppedFake  *atomic.Int64       // 因跳转首页/登录页被剔除的数量（指针：支持每个目标复制独立计数）

	// 请求定制与身份模拟
	Headers     map[string]string // 自定义请求头（Name: Value，逐条 Set）
	Body        []byte            // 自定义请求体（配合 -m POST/PUT 等）
	ContentType string            // 请求体 Content-Type（空 = application/x-www-form-urlencoded）
	ProxyURL    *url.URL          // HTTP/HTTPS/SOCKS5 代理
	UAMode      UAMode            // UA 选择模式（--ua）
	CustomUA    string            // 自定义固定 UA
	SessionUA   string            // 会话级 UA（每目标解析一次；非空时该目标所有请求固定使用）
	TlsVerify   bool              // 校验 TLS 证书（默认跳过自签名/过期证书）

	// 复用的 HTTP 客户端缓存（follow / no-follow 两个变体）：
	// 递归各层、校准/伪重定向/指纹学习请求共享同一连接池，避免反复 TLS 握手。
	// 独立结构体 + 指针：Scanner 按目标复制时锁不被拷贝，各目标持有独立缓存。
	clientCache *httpClientCache

	// 被动情报收集
	Extract    bool        // 被动提取：响应内的链接/JS/robots/sitemap/注释 → 新路径自动入队扫描
	ExtractMax int         // 每目标被动提取路径上限（0 = 默认 500）
	Discovered *atomic.Int64 // 实际入队的被动发现数（指针：支持每个目标复制独立计数）
}

// ctx 返回扫描请求上下文：Ctrl+C 取消时立即终止在途请求；未设置时为 Background
func (s *Scanner) ctx() context.Context {
	if s.Ctx == nil {
		return context.Background()
	}
	return s.Ctx
}

// stopped 返回扫描是否已被中断（Ctrl+C 置位 Stop 或 ctx 已取消）
func (s *Scanner) stopped() bool {
	if s.Stop != nil && s.Stop.Load() {
		return true
	}
	return s.ctx().Err() != nil
}

// concurrencyGate 动态并发闸门：允许并发上限在运行期调整（--auto）。
// 用容量 1 的 release 信号信道替代 sync.Cond：acquire 可被 ctx 取消，
// 避免 Ctrl+C 时 worker 永久阻塞在闸门上。
type concurrencyGate struct {
	mu       sync.Mutex
	wake     chan struct{} // 容量 1 的唤醒信号（非阻塞发送）
	limit    int
	inflight int
}

func newGate(limit int) *concurrencyGate {
	g := &concurrencyGate{limit: limit, wake: make(chan struct{}, 1)}
	return g
}

func (g *concurrencyGate) acquire() {
	g.acquireCtx(context.Background())
}

// acquireCtx 获取一个并发额度；ctx 被取消时返回 false（不占用额度）
func (g *concurrencyGate) acquireCtx(ctx context.Context) bool {
	g.mu.Lock()
	for g.inflight >= g.limit {
		g.mu.Unlock()
		select {
		case <-g.wake:
		case <-ctx.Done():
			return false
		}
		g.mu.Lock()
	}
	g.inflight++
	g.mu.Unlock()
	return true
}

func (g *concurrencyGate) release() {
	g.mu.Lock()
	g.inflight--
	g.mu.Unlock()
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

func (g *concurrencyGate) setLimit(n int) {
	if n < 1 {
		n = 1
	}
	g.mu.Lock()
	g.limit = n
	g.mu.Unlock()
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// 运行期统计（供自适应调优）
type scanStats struct {
	done    atomic.Int64 // 已执行请求数
	errs    atomic.Int64 // 网络失败数
	latSum  atomic.Int64 // 请求总耗时（毫秒）
}

// Scan 扫描所有路径，返回通过过滤的结果（已按 URL 排序）。
// 支持被动提取：Extract=true 时，2xx 响应的链接/JS/robots/sitemap/注释中提取的新路径
// 会去重后动态入队继续扫描（受 ExtractMax 上限约束），Discover 统计入队的发现数。
func (s *Scanner) Scan(baseURL string, paths []string) []Result {
	total := len(paths)
	if total == 0 {
		return nil
	}

	client := s.httpClient(s.Follow)
	// worker 池按最大并发创建（--auto 时为 auto-max，默认 4×-c），实际并发由
	// gate 从 -c 起步在运行期调整：--auto 既能降也能升；非 auto 时池大小 ==
	// gate 上限 == -c，行为与历史一致。
	poolSize := s.Concurrency
	autoMax := s.Concurrency
	if s.Auto {
		autoMax = s.AutoMax
		if autoMax <= 0 {
			autoMax = s.Concurrency * 4
		}
		if autoMax < s.Concurrency {
			autoMax = s.Concurrency
		}
		poolSize = autoMax
		if tr, ok := client.Transport.(*http.Transport); ok {
			tr.MaxIdleConnsPerHost = poolSize
		}
	}
	gate := newGate(s.Concurrency)
	var stats scanStats

	maxDiscover := s.ExtractMax
	if maxDiscover <= 0 {
		maxDiscover = 500
	}
	// 任务队列容量 ≥ 全部种子 + 提取上限：保证提取入队永不因缓冲区满而回滚丢弃
	jobBuf := total + maxDiscover + poolSize*2
	jobs := make(chan string, jobBuf)
	results := make(chan Result)

	// 动态任务计数：pending=待处理（含队列内），inFlight=正在处理
	var pending, inFlight atomic.Int64
	pending.Store(int64(total))
	seen := &sync.Map{}
	var discovered atomic.Int64

	done := make(chan struct{})
	var doneOnce sync.Once
	tryDone := func() {
		if pending.Load() == 0 && inFlight.Load() == 0 {
			doneOnce.Do(func() { close(done) })
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < poolSize; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if s.Stop != nil && s.Stop.Load() {
					return
				}
				select {
				case <-s.ctx().Done(): // Ctrl+C：立即退出，不再领取新任务
					return
				case path := <-jobs:
					inFlight.Add(1)
					pending.Add(-1)
					if s.Stop != nil && s.Stop.Load() {
						inFlight.Add(-1)
						tryDone()
						continue
					}
					if s.Rate != nil && s.Rate.WaitCtx(s.ctx()) < 0 {
						// 限速等待期间被取消
						inFlight.Add(-1)
						tryDone()
						return
					}
					if !gate.acquireCtx(s.ctx()) {
						// 闸门等待期间被取消
						inFlight.Add(-1)
						tryDone()
						return
					}
					r, ok, dur := s.probe(client, baseURL, path)
					gate.release()

					stats.done.Add(1)
					stats.latSum.Add(dur.Milliseconds())
					if !ok {
						stats.errs.Add(1)
					}

					// 被动提取：2xx 响应回传的 body 内挖新路径，去重后入队
					if ok && s.Extract && len(r.Body) > 0 && r.Status >= 200 && r.Status < 300 {
						for _, np := range s.extractPaths(r.Body, r.ContentType, r.URL) {
							if _, dup := seen.LoadOrStore(np, struct{}{}); dup {
								continue
							}
							if discovered.Load() >= int64(maxDiscover) {
								continue
							}
							discovered.Add(1)
							pending.Add(1)
							select {
							case jobs <- np:
								s.Discovered.Add(1)
							default: // 队列容量保证不会走到；兜底回滚
								pending.Add(-1)
								discovered.Add(-1)
							}
						}
						r.Body = nil // 及时释放
					}

					if ok && s.keepFakeRedirect(r) && s.keepByBaseline(r) && shouldShow(r, s.Show, s.Hide) {
						results <- r
					}
					if !s.Quiet {
						if s.Progress != nil {
							s.Progress.Tick()
						} else if n := stats.done.Load(); n%50 == 0 {
							printProgress(n, int64(total)+discovered.Load())
						}
					}
					inFlight.Add(-1)
					tryDone()
				default:
					if pending.Load() == 0 && inFlight.Load() == 0 {
						tryDone()
						return
					}
					time.Sleep(2 * time.Millisecond)
				}
			}
		}()
	}

	go func() {
		for _, p := range paths {
			if s.Stop != nil && s.Stop.Load() {
				return
			}
			select {
			case <-s.ctx().Done():
				return
			default:
			}
			// 阻塞发送保证全部种子入队：pending 计数与 jobs 内任务严格一致，
			// 否则 buffer 满时丢任务会留下永不消费的 pending，导致 worker 空转死等。
			select {
			case jobs <- p:
			case <-s.ctx().Done():
				return
			}
		}
	}()
	go func() {
		if !(s.Stop != nil && s.Stop.Load()) {
			select {
			case <-done:
			case <-s.ctx().Done():
			}
		}
		wg.Wait()
		close(results)
	}()

	if s.Auto {
		go s.autoTune(gate, &stats, done, s.Concurrency, autoMax)
	}

	var out []Result
	for r := range results {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// httpClientCache 缓存 HTTP 客户端（follow / no-follow），共享 Transport 连接池
type httpClientCache struct {
	mu      sync.Mutex
	clients map[bool]*http.Client
}

func (c *httpClientCache) get(follow bool, build func() *http.Client) *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clients == nil {
		c.clients = make(map[bool]*http.Client)
	}
	if cl, ok := c.clients[follow]; ok {
		return cl
	}
	cl := build()
	c.clients[follow] = cl
	return cl
}

// httpClient 返回缓存的 HTTP 客户端（follow / no-follow 两个变体）。
// 复用 Transport 连接池：递归各层与校准/伪重定向/指纹学习共享 keep-alive 连接。
func (s *Scanner) httpClient(follow bool) *http.Client {
	if s.clientCache == nil {
		s.clientCache = &httpClientCache{}
	}
	return s.clientCache.get(follow, func() *http.Client { return s.newClient(follow) })
}

// newClient 构建 HTTP 客户端（Scan / 基准学习 / 指纹学习共用）。
// follow=true：跟随重定向并在 CheckRedirect 中收集重定向链；
// follow=false：不跟随，仅透出首个 Location。
func (s *Scanner) newClient(follow bool) *http.Client {
	transport := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: !s.TlsVerify}, // --tls-verify 开启校验
		MaxIdleConnsPerHost: s.Concurrency,
		IdleConnTimeout:     30 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	if s.MaxConns > 0 {
		transport.MaxConnsPerHost = s.MaxConns
	}
	if s.ProxyURL != nil {
		transport.Proxy = http.ProxyURL(s.ProxyURL)
	}

	client := &http.Client{
		Timeout:   s.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !follow {
				return http.ErrUseLastResponse // 不跟随，记录 Location
			}
			// 跟随：把本次跳转目标追加到该请求的链容器
			if chain, ok := req.Context().Value(redirectChainKey).(*[]string); ok {
				*chain = append(*chain, req.URL.String())
			}
			return nil
		},
	}
	return client
}

// autoTune 自适应并发：每 2s 观察窗口内的错误率与平均延迟，
// 错误率升高/延迟变长 → 降低并发；延迟低且稳定 → 缓慢提升并发。
// 扫描完成（scanDone 关闭）或 ctx 取消（Ctrl+C）时退出，避免 goroutine 泄漏。
func (s *Scanner) autoTune(gate *concurrencyGate, st *scanStats, scanDone <-chan struct{}, cur, maxLimit int) {
	prevDone, prevErrs, prevLat := int64(0), int64(0), int64(0)
	prevAvgLat := 0.0
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-s.ctx().Done():
			return
		case <-scanDone:
			return
		case <-tick.C:
		}
		done := st.done.Load()
		errs := st.errs.Load()
		lat := st.latSum.Load()

		winDone := done - prevDone
		winErrs := errs - prevErrs
		winLat := lat - prevLat
		prevDone, prevErrs, prevLat = done, errs, lat

		if winDone <= 0 {
			// 无进展：可能是限速/排队/全部完成
			continue
		}
		curAvgLat := float64(winLat) / float64(winDone)
		errRate := float64(winErrs) / float64(winDone)

		switch {
		case errRate > 0.2:
			cur = max(1, cur/2)
		case prevAvgLat > 0 && curAvgLat > prevAvgLat*1.5:
			cur = max(1, int(float64(cur)*0.8))
		case prevAvgLat > 0 && curAvgLat < prevAvgLat*0.8 && errRate < 0.05:
			cur = min(maxLimit, cur+5)
		}
		prevAvgLat = curAvgLat
		gate.setLimit(cur)
	}
}

// probe 单路径探测（含重试）
func (s *Scanner) probe(client *http.Client, base, path string) (Result, bool, time.Duration) {
	u := joinURL(base, path)
	start := time.Now()

	for attempt := 0; ; attempt++ {
		var bodyReader io.Reader
		if len(s.Body) > 0 {
			bodyReader = bytes.NewReader(s.Body)
		}
		req, err := http.NewRequestWithContext(s.ctx(), s.Method, u, bodyReader)
		if err != nil {
			return Result{}, false, time.Since(start)
		}
		req.Header.Set("User-Agent", s.userAgent())
		if s.Cookie != "" {
			req.Header.Set("Cookie", s.Cookie)
		}
		for name, val := range s.Headers {
			req.Header.Set(name, val)
		}
		if len(s.Body) > 0 {
			ct := s.ContentType
			if ct == "" {
				ct = "application/x-www-form-urlencoded"
			}
			req.Header.Set("Content-Type", ct)
		}
		req.Header.Set("Accept", "*/*")
		// 跟随模式：挂载重定向链容器
		var chain []string
		if s.Follow {
			chain = make([]string, 0, 4)
			req = req.WithContext(withRedirectChain(req.Context(), &chain))
		}

		resp, err := client.Do(req)
		if err == nil {
			// 服务端 5xx：按 MaxRetries 重试（网络错误同样重试）
			if resp.StatusCode >= 500 && attempt < s.MaxRetries {
				resp.Body.Close()
				sleepBackoff(attempt)
				continue
			}
			r := s.collect(resp, u, chain)
			r.TimeMs = time.Since(start).Milliseconds()
			return r, true, time.Since(start)
		}

		// 网络失败：按 MaxRetries 重试，指数退避
		if attempt >= s.MaxRetries {
			return Result{}, false, time.Since(start)
		}
		select {
		case <-s.ctx().Done(): // 取消后不再退避等待，立即返回
			return Result{}, false, time.Since(start)
		default:
		}
		sleepBackoff(attempt)
	}
}

// sleepBackoff 指数退避：100ms、200ms、400ms…上限 1s
func sleepBackoff(attempt int) {
	backoff := time.Duration(100*(1<<attempt)) * time.Millisecond
	if backoff > time.Second {
		backoff = time.Second
	}
	time.Sleep(backoff)
}

// collect 读取响应并提取字段
func (s *Scanner) collect(resp *http.Response, u string, chain []string) Result {
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // body 上限 2MB

	r := Result{
		URL:      u,
		Status:   resp.StatusCode,
		Size:     int64(len(body)),
		Location: resp.Header.Get("Location"),
		Server:   resp.Header.Get("Server"),
	}
	if len(chain) > 0 {
		r.Chain = chain
		r.FinalURL = chain[len(chain)-1]
	}
	if resp.ContentLength > 0 {
		r.Size = resp.ContentLength
	}
	if len(body) > 0 && r.Status != 404 {
		r.Title = extractTitle(string(body))
	}
	if s.ComputeHash && len(body) > 0 {
		r.Hash = hashBody(body)
	}
	if s.Extract {
		r.Body = body
		r.ContentType = resp.Header.Get("Content-Type")
	}
	return r
}

// shouldShow 状态码过滤：白名单优先，其次黑名单
func shouldShow(r Result, show, hide map[int]bool) bool {
	if len(show) > 0 {
		return show[r.Status]
	}
	return !hide[r.Status]
}

// keepByBaseline 返回响应是否应保留（非"无效页面"）。
// 基准关闭时恒为 true（不过滤）。
func (s *Scanner) keepByBaseline(r Result) bool {
	if s.Baseline == nil {
		return true
	}
	return !s.Baseline.Match(r)
}

// keepFakeRedirect 返回响应是否应保留（非"跳转首页/登录页"的伪有效）。
// 过滤器关闭时恒为 true；命中时计入 DroppedFake 统计。
func (s *Scanner) keepFakeRedirect(r Result) bool {
	if s.FakeRedirect == nil {
		return true
	}
	if s.FakeRedirect.Match(r) {
		s.DroppedFake.Add(1)
		return false
	}
	return true
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// extractTitle 提取 <title> 并去除内嵌标签与空白
func extractTitle(html string) string {
	m := titleRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return ""
	}
	t := strings.ReplaceAll(m[1], "\n", " ")
	t = strings.ReplaceAll(t, "\r", " ")
	t = strings.TrimSpace(t)
	if len(t) > 80 {
		t = t[:80]
	}
	return t
}

// joinURL 拼接 base 与路径：base 末尾去斜杠；path 为完整 URL 时原样返回
func joinURL(base, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return strings.TrimRight(base, "/") + path
}