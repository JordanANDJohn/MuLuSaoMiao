package main

import (
	"net/url"
	"regexp"
	"strings"
)

// 被动情报收集：从扫描响应中提取新路径，反哺扫描队列。
//
// 来源：
//   - HTML：href/src/action/poster/data-url 属性，HTML 注释内的 URL/路径
//   - robots.txt：Allow / Disallow / Sitemap 行
//   - sitemap.xml：<loc> 条目
//   - JavaScript / JSON：引号包裹的绝对 URL 与 / 开头路径字符串
//
// 所有提取结果统一规范化为「以 / 开头的同源相对路径」：
// 相对路径基于页面 URL 解析；绝对 URL 要求与目标同 host；丢弃 query/fragment；
// 过滤静态资源（图片/字体/视频等）与伪协议（javascript: 等）。

var (
	htmlAttrRe    = regexp.MustCompile(`(?is)(?:href|src|action|poster|data-url)\s*=\s*["']([^"']+)["']`)
	htmlCommentRe = regexp.MustCompile(`(?s)<!--(.*?)-->`)
	absURLRe      = regexp.MustCompile(`https?://[^\s"'<>]+`)
	relPathRe     = regexp.MustCompile(`/[A-Za-z0-9_\-./@%?=&~+]{1,256}`)
	jsURLRe       = regexp.MustCompile(`["']((?:https?://|/)[^"'\s<>]{2,256})["']`)
	robotsLineRe  = regexp.MustCompile(`(?mi)^\s*(Allow|Disallow|Sitemap)\s*:\s*(.+?)\s*$`)
	sitemapLocRe  = regexp.MustCompile(`(?is)<loc[^>]*>(.*?)</loc>`)
	staticExtRe   = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|webp|bmp|svg|ico|css|woff2?|eot|ttf|otf|mp4|mp3|webm|avi|mov|zip|gz|tar|7z|rar|exe|msi|apk|pdf|docx?|xlsx?|pptx?)$`)
)

// extractPaths 从单个响应 body 中提取可扫描路径（已规范化、页内去重）。
// 仅在响应为 2xx 且 body 非空时由扫描循环调用。
func (s *Scanner) extractPaths(body []byte, contentType, pageURLStr string) []string {
	if len(body) == 0 {
		return nil
	}
	pageURL, err := url.Parse(pageURLStr)
	if err != nil {
		return nil
	}
	ct := strings.ToLower(contentType)
	pagePath := strings.ToLower(pageURL.Path)
	lower := string(body)

	var raw []string
	switch {
	case strings.Contains(ct, "text/plain") && strings.HasSuffix(pagePath, "/robots.txt"):
		for _, m := range robotsLineRe.FindAllStringSubmatch(lower, -1) {
			raw = append(raw, strings.TrimSpace(m[2]))
		}
	case strings.Contains(ct, "xml") || strings.Contains(pagePath, "sitemap"):
		for _, m := range sitemapLocRe.FindAllStringSubmatch(lower, -1) {
			raw = append(raw, m[1])
		}
	case strings.Contains(ct, "javascript"):
		for _, m := range jsURLRe.FindAllStringSubmatch(lower, -1) {
			raw = append(raw, m[1])
		}
	case strings.Contains(ct, "json"):
		for _, m := range jsURLRe.FindAllStringSubmatch(lower, -1) {
			raw = append(raw, m[1])
		}
	default: // HTML 或未知类型：属性链接 + 注释内 URL/路径
		for _, m := range htmlAttrRe.FindAllStringSubmatch(lower, -1) {
			raw = append(raw, m[1])
		}
		for _, cm := range htmlCommentRe.FindAllStringSubmatch(lower, -1) {
			for _, m := range absURLRe.FindAllStringSubmatch(cm[1], -1) {
				raw = append(raw, m[0])
			}
			for _, m := range relPathRe.FindAllStringSubmatch(cm[1], -1) {
				raw = append(raw, m[0])
			}
		}
	}

	seenLocal := make(map[string]bool)
	var out []string
	for _, r := range raw {
		p, ok := normalizePath(r, pageURL)
		if !ok || seenLocal[p] {
			continue
		}
		seenLocal[p] = true
		out = append(out, p)
	}
	return out
}

// normalizePath 把提取到的原始链接规范化为同源绝对路径（以 / 开头）。
// 规则：伪协议/片段/跨域丢弃；相对路径基于页面 URL 解析；去掉 query/fragment 与尾斜杠；
// 静态媒体资源与根路径 "/" 丢弃。
func normalizePath(raw string, pageURL *url.URL) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 {
		return "", false
	}
	low := strings.ToLower(raw)
	for _, prefix := range []string{"javascript:", "mailto:", "tel:", "data:", "vbscript:", "about:"} {
		if strings.HasPrefix(low, prefix) {
			return "", false
		}
	}
	if strings.HasPrefix(raw, "#") || strings.ContainsAny(raw, " \t\r\n<>\"'") {
		return "", false
	}

	ref, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if ref.IsAbs() {
		// 绝对 URL：仅接受 http/https 且与当前页同 host
		if (ref.Scheme != "http" && ref.Scheme != "https") || !strings.EqualFold(ref.Host, pageURL.Host) {
			return "", false
		}
	} else {
		ref = pageURL.ResolveReference(ref)
		if !strings.EqualFold(ref.Host, pageURL.Host) {
			return "", false
		}
	}

	p := ref.Path
	if p == "" || p == "/" {
		return "", false
	}
	p = strings.TrimSuffix(p, "/")
	if isStaticAsset(p) {
		return "", false
	}
	return p, true
}

// isStaticAsset 判断路径是否指向静态媒体资源（图片/字体/视频/安装包/文档等）
func isStaticAsset(p string) bool {
	base := p[strings.LastIndex(p, "/")+1:]
	if !strings.Contains(base, ".") {
		return false
	}
	return staticExtRe.MatchString(base)
}