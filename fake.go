package main

import (
	"io"
	"net/http"
	"strings"
)

// FakeRedirectFilter 伪有效目录过滤器：
// 很多站点对所有不存在路径统一 302 → 首页（/）或登录页（/login），
// 这些重定向看似"有效"（3xx + Location），实则毫无价值。
// 学习阶段跟随请求首页/登录页，记录其落地 URL 集合，扫描时命中即剔除。
type FakeRedirectFilter struct {
	targets map[string]struct{} // 首页/登录页归一化落地 URL
	probes  []string            // 已成功学习的探测 URL（展示用）
	base    string              // 学习目标 base（用于相对 Location 补齐）
}

// NormalizeRedirectURL 归一化 URL 用于比较：
// 去 query/fragment、去默认端口、scheme 统一小写、去尾部斜杠。
// 返回"origin+path"（路径结尾斜杠视为等价）。
func normalizeRedirectURL(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	u = strings.TrimSuffix(u, ":80")
	u = strings.TrimSuffix(u, ":443")
	u = strings.TrimRight(strings.ToLower(u), "/")
	return u
}

// NewFakeRedirectFilter 创建空过滤器
func NewFakeRedirectFilter() *FakeRedirectFilter {
	return &FakeRedirectFilter{targets: make(map[string]struct{})}
}

// LearnFakeRedirects 学习目标的"首页/登录页指纹"。
// 跟随 GET 探测 /、/index.html、/login，记录最终落地 URL 集合。
// 全部探测失败时返回 nil（目标不可达，不启用过滤）。
func LearnFakeRedirects(s *Scanner, baseURL string) *FakeRedirectFilter {
	f := NewFakeRedirectFilter()
	f.base = strings.TrimRight(baseURL, "/")
	// 学习阶段总是跟随重定向，拿到"最终落地"URL 作指纹
	client := s.httpClient(true)

	for _, p := range []string{"/", "/index.html", "/login"} {
		if s.ctx().Err() != nil { // 中断：停止学习
			break
		}
		chain, final, status, err := probeRedirect(client, s, baseURL, p)
		if err != nil || status == 0 {
			continue
		}
		reqURL := joinURL(baseURL, p)
		// 落地 URL（跟随链末尾）；未跳转时落地即自身
		landing := final
		if landing == "" || normalizeRedirectURL(landing) == "" {
			landing = reqURL
		}
		f.targets[normalizeRedirectURL(landing)] = struct{}{}
		// 若存在重定向链，把链上每一跳也都记为指纹（登录→首页两步跳等）
		for _, hop := range chain {
			f.targets[normalizeRedirectURL(hop)] = struct{}{}
		}
		f.probes = append(f.probes, reqURL)
	}

	if len(f.probes) == 0 {
		return nil
	}
	return f
}

// probeRedirect 跟随请求目标路径，返回重定向链、最终落地 URL 与最终状态码
func probeRedirect(client *http.Client, s *Scanner, base, path string) ([]string, string, int, error) {
	u := joinURL(base, path)
	req, err := http.NewRequestWithContext(s.ctx(), http.MethodGet, u, nil)
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("User-Agent", s.userAgent())
	if s.Cookie != "" {
		req.Header.Set("Cookie", s.Cookie)
	}
	for name, val := range s.Headers {
		req.Header.Set(name, val)
	}
	chain := make([]string, 0, 4)
	req = req.WithContext(withRedirectChain(req.Context(), &chain))

	if s.Rate != nil && s.Rate.WaitCtx(s.ctx()) < 0 { // 与扫描一致走限速
		return nil, "", 0, io.EOF
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()

	final := u
	if len(chain) > 0 {
		final = chain[len(chain)-1]
	} else if loc := resp.Header.Get("Location"); loc != "" {
		final = loc
	}
	return chain, final, resp.StatusCode, nil
}

// Match 判断结果是否属于"跳转到首页/登录页"的伪有效响应。
// 跟随模式用最终落地 URL，未跟随模式用首个 Location（相对路径自动按 base 补齐）。
// 请求本身命中指纹（即首页/登录页自身）时视为真实页面，不视为伪有效。
func (f *FakeRedirectFilter) Match(r Result) bool {
	if f == nil || len(f.targets) == 0 {
		return false
	}
	// 请求的就是首页/登录页本身 → 真实响应
	if _, self := f.targets[normalizeRedirectURL(r.URL)]; self {
		return false
	}
	if r.FinalURL != "" {
		_, ok := f.targets[normalizeRedirectURL(r.FinalURL)]
		return ok
	}
	if r.Location != "" {
		loc := r.Location
		if strings.HasPrefix(loc, "/") && f.base != "" {
			loc = f.base + loc
		}
		_, ok := f.targets[normalizeRedirectURL(loc)]
		return ok
	}
	return false
}

// Summary 返回学习摘要（展示用）
func (f *FakeRedirectFilter) Summary() string {
	if f == nil {
		return "学习失败（目标不可达）"
	}
	return strings.Join(f.probes, ", ")
}