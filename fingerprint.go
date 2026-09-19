package main

import (
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Fingerprint 目标技术指纹：从首页/robots.txt 请求中提取的特征集合。
//
// Matches(k) 对任意特征做大小写不敏感的包含匹配——供字典模块装配
// （模块元数据 match= 关键词命中任一特征即装配）。
type Fingerprint struct {
	Target     string   // 归一化目标
	OK         bool     // 指纹探测是否成功（首页可达）
	Status     int      // 首页响应状态码
	Title      string   // <title>
	Server     string   // Server 头
	PoweredBy  string   // X-Powered-By
	Cookies    []string // Set-Cookie 名称
	Generators []string // <meta name="generator"> 内容
	BodyPeek   string   // 响应体特征片段（小写，截断）
	Robots     []string // robots.txt 中有意义的行（Disallow/User-agent）
	HeaderHits []string // 有指纹价值的响应头（x-* 等）
}

// 预编译的正则：HTML title 与 generator 提取
var (
	reTitle     = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reGenerator = regexp.MustCompile(`(?is)<meta[^>]+name=["']generator["'][^>]+content=["']([^"']*?)["']`)
	reGen2      = regexp.MustCompile(`(?is)<meta[^>]+content=["']([^"']*?)["'][^>]+name=["']generator["']`)
	reControl   = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f]`)
)

// InferFingerprint 对目标执行轻量指纹探测（复用 Scanner 的传输/头/代理配置）。
// 请求：首页（GET /）+ robots.txt；不跟随重定向。
func InferFingerprint(sc *Scanner, target string) *Fingerprint {
	fp := &Fingerprint{Target: target}
	client := sc.httpClient(false)

	body, err := fetchFingerprint(client, sc, target, "/", fp)
	if err != nil {
		return fp // 首页不可达 → OK=false
	}
	fp.OK = true
	fp.analyze(string(body))

	// robots.txt：失败不致命；仅保留有意义的行
	if rb, rerr := fetchFingerprint(client, sc, target, "/robots.txt", fp); rerr == nil {
		for _, line := range strings.Split(string(rb), "\n") {
			l := strings.ToLower(strings.TrimSpace(line))
			if strings.HasPrefix(l, "disallow") || strings.HasPrefix(l, "user-agent") || strings.HasPrefix(l, "allow") {
				fp.Robots = append(fp.Robots, strings.TrimSpace(line))
			}
		}
	}
	return fp
}

// fetchFingerprint 发起单次指纹请求，把 header/cookie 特征写入 fp，返回响应体。
func fetchFingerprint(client *http.Client, sc *Scanner, target, path string, fp *Fingerprint) ([]byte, error) {
	u := joinURL(target, path)
	req, err := http.NewRequestWithContext(sc.ctx(), "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", sc.userAgent())
	req.Header.Set("Accept", "*/*")
	if sc.Cookie != "" {
		req.Header.Set("Cookie", sc.Cookie)
	}
	for name, val := range sc.Headers {
		req.Header.Set(name, val)
	}
	if sc.Rate != nil && sc.Rate.WaitCtx(sc.ctx()) < 0 {
		return nil, io.EOF
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return nil, nil
	}
	if fp.Status == 0 {
		fp.Status = resp.StatusCode
	}
	if s := resp.Header.Get("Server"); s != "" && fp.Server == "" {
		fp.Server = s
	}
	if p := resp.Header.Get("X-Powered-By"); p != "" && fp.PoweredBy == "" {
		fp.PoweredBy = p
	}
	for _, cc := range resp.Header.Values("Set-Cookie") {
		name := cc
		if i := strings.Index(cc, "="); i > 0 {
			name = cc[:i]
		}
		name = strings.TrimSpace(name)
		fp.Cookies = append(fp.Cookies, name)
	}
	for h, v := range resp.Header {
		lh := strings.ToLower(h)
		if len(v) == 0 {
			continue
		}
		if strings.HasPrefix(lh, "x-") || lh == "server" {
			fp.HeaderHits = append(fp.HeaderHits, lh+": "+strings.Join(v, "; "))
		}
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if rerr != nil {
		return nil, rerr
	}
	return body, nil
}

// analyze 从响应体提取 title/generator，并收集 body 特征文本
func (fp *Fingerprint) analyze(body string) {
	if m := reTitle.FindStringSubmatch(body); len(m) > 1 {
		fp.Title = strings.TrimSpace(m[1])
	}
	if m := reGenerator.FindStringSubmatch(body); len(m) > 1 && m[1] != "" {
		fp.Generators = append(fp.Generators, m[1])
	} else if m := reGen2.FindStringSubmatch(body); len(m) > 1 && m[1] != "" {
		fp.Generators = append(fp.Generators, m[1])
	}
	// body 特征片段：去控制字符、转小写、截断至 4KB（供关键词包含匹配）
	clean := reControl.ReplaceAllString(body, "")
	fp.BodyPeek = strings.ToLower(truncateBytes(clean, 4096))
}

// truncateBytes 按字节截断
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Matches 判断关键词是否命中指纹的任一特征（大小写不敏感，词边界匹配）。
// 短关键词（如 gin/dav）只做整词匹配，避免命中长字符串的子串（nginx 含 gin）。
func (fp *Fingerprint) Matches(k string) bool {
	if k == "" {
		return false
	}
	re := keywordRegex(k)
	if re == nil {
		return false
	}
	if re.MatchString(fp.BodyPeek) {
		return true
	}
	if fp.Server != "" && re.MatchString(strings.ToLower(fp.Server)) {
		return true
	}
	if fp.PoweredBy != "" && re.MatchString(strings.ToLower(fp.PoweredBy)) {
		return true
	}
	if fp.Title != "" && re.MatchString(strings.ToLower(fp.Title)) {
		return true
	}
	for _, g := range fp.Generators {
		if re.MatchString(strings.ToLower(g)) {
			return true
		}
	}
	for _, c := range fp.Cookies {
		if re.MatchString(strings.ToLower(c)) {
			return true
		}
	}
	for _, r := range fp.Robots {
		if re.MatchString(strings.ToLower(r)) {
			return true
		}
	}
	for _, h := range fp.HeaderHits {
		if re.MatchString(strings.ToLower(h)) {
			return true
		}
	}
	return false
}

// keywordRegex 编译关键词的词边界匹配正则（缓存：关键词数量有限）
// 特殊字符照常转义，\b 仅适用于词字符类关键词，故让步非词字符（如 "/public/index"）用包含匹配
func keywordRegex(k string) *regexp.Regexp {
	if k == "" {
		return nil
	}
	if len(k) < 4 {
		// 短词（<4 字符）不参与匹配，避免误报（更稳妥的装配宁可少不可错）
		return nil
	}
	esc := regexp.QuoteMeta(k)
	// \b 需要词字符边界；对含非词字符的关键词退化为包含匹配
	hasNonWord := regexp.MustCompile(`[^a-zA-Z0-9_]`).MatchString(k)
	if hasNonWord {
		return regexp.MustCompile(`(?i)` + esc)
	}
	return regexp.MustCompile(`(?i)\b` + esc + `\b`)
}

// Summary 人类可读指纹摘要（--fingerprint 输出）
func (fp *Fingerprint) Summary() string {
	var parts []string
	if !fp.OK {
		return "不可达"
	}
	if fp.Status != 0 {
		parts = append(parts, "状态 "+itoa(fp.Status))
	}
	if fp.Server != "" {
		parts = append(parts, "Server: "+fp.Server)
	}
	if fp.PoweredBy != "" {
		parts = append(parts, "X-Powered-By: "+fp.PoweredBy)
	}
	if len(fp.Generators) > 0 {
		parts = append(parts, "generator: "+strings.Join(fp.Generators, ", "))
	}
	if len(fp.Cookies) > 0 {
		parts = append(parts, "cookie: "+strings.Join(fp.Cookies, ", "))
	}
	if fp.Title != "" {
		parts = append(parts, "title: "+fp.Title)
	}
	if len(parts) == 0 {
		return "无可识别特征"
	}
	return strings.Join(parts, " | ")
}

// itoa 简易整数转字符串
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}