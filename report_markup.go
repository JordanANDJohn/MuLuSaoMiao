package main

import (
	"fmt"
	"os"
	"strings"
)

// htmlEscape HTML 文本转义
func htmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// statusClass 状态码 CSS 分类（与终端着色一致）
func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "s2xx"
	case status >= 300 && status < 400:
		return "s3xx"
	case status == 401 || status == 403:
		return "s4auth"
	case status >= 400 && status < 500:
		return "s4xx"
	default:
		return "s5xx"
	}
}

// htmlSection 渲染一个风险分类：编号标题 + 提示 + 表格 + 本类 URL 列表
func htmlSection(no int, c ReportCategory) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<h2 class="cat" id="cat%s">%d. %s <span class="badge">%s · %d 条</span></h2>`, c.Key, no, htmlEscape(c.Key), htmlEscape(c.Title), len(c.Results)))
	b.WriteString(fmt.Sprintf(`<p class="desc">%s</p>`, htmlEscape(c.Desc)))
	if len(c.Results) == 0 {
		b.WriteString(`<p class="empty">（无命中）</p>`)
		return b.String()
	}
	b.WriteString(`<table><thead><tr><th>状态</th><th>URL</th><th>大小</th><th>耗时(ms)</th><th>标题</th><th>重定向</th><th>Server</th></tr></thead><tbody>`)
	for _, r := range c.Results {
		b.WriteString(fmt.Sprintf(`<tr class="%s">
<td class="st">%d</td>
<td class="url">%s</td>
<td>%s</td>
<td>%d</td>
<td>%s</td>
<td>%s</td>
<td>%s</td>
</tr>
`, statusClass(r.Status), r.Status, htmlEscape(r.URL), humanSize(r.Size), r.TimeMs,
			htmlEscape(r.Title), htmlEscape(redirectDesc(r)), htmlEscape(r.Server)))
	}
	b.WriteString(`</tbody></table>`)
	// 本分类 URL 列表（便于复制）
	b.WriteString(fmt.Sprintf(`<div class="urls-block"><h3>本类全部 URL（%d）</h3><pre>%s</pre></div>`,
		len(c.URLs), htmlEscape(strings.Join(c.URLs, "\n"))))
	return b.String()
}

// SaveHTMLReport 导出 .html（自包含页面：6 类风险分组 + 说明，只收录有价值结果）
func SaveHTMLReport(rep ReportData, outPath string) error {
	sects := make([]string, 0, 8)
	for i, c := range rep.Cats {
		sects = append(sects, htmlSection(i+1, c))
	}

	all := AllURLs(rep)
	var allBlock string
	if len(all) > 0 {
		allBlock = fmt.Sprintf(`<h2 class="cat all">全部 URL 汇总（%d）</h2><pre class="urls-all">%s</pre>`,
			len(all), htmlEscape(strings.Join(all, "\n")))
	}

	doc := `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>MuLuSaoMiao 扫描报告</title>
<style>
body { font-family: "Segoe UI", "Microsoft YaHei", sans-serif; margin: 24px; color: #333; }
h1 { border-bottom: 2px solid #eee; padding-bottom: 8px; }
h2.cat { background: #f7f7f7; padding: 8px 12px; border-left: 4px solid #666; margin-top: 28px; }
h2.cat.all { border-left-color: #1a7f37; }
h3 { color: #444; margin: 14px 0 6px; }
.badge { font-size: 12px; color: #666; font-weight: normal; }
.desc { color: #666; font-size: 13px; margin: 4px 0 12px; }
.empty { color: #999; font-style: italic; }
.meta { color: #666; margin-bottom: 16px; }
table { border-collapse: collapse; width: 100%; font-size: 13px; }
th, td { border: 1px solid #ddd; padding: 6px 8px; text-align: left; }
th { background: #f5f5f5; }
tr:nth-child(even) { background: #fafafa; }
.st { font-weight: bold; }
.s2xx .st { color: #1a7f37; }
.s3xx .st { color: #9a6700; }
.s4auth .st { color: #0969da; }
.s4xx .st { color: #bc4c00; }
.s5xx .st { color: #cf222e; }
.url { word-break: break-all; }
.urls-block { margin: 8px 0 4px; }
.urls-block pre, .urls-all { background: #f6f8fa; border: 1px solid #ddd; border-radius: 4px; padding: 10px 12px; font-size: 12px; overflow-x: auto; white-space: pre-wrap; word-break: break-all; user-select: all; }
.notice { margin-top: 24px; padding: 10px 14px; background: #fff8e1; border: 1px solid #f0e0a0; color: #7a5b00; border-radius: 4px; }
</style>
</head>
<body>
<h1>MuLuSaoMiao 扫描报告</h1>
<div class="meta">
目标: ` + htmlEscape(rep.Target) + `<br>
请求方法: ` + htmlEscape(rep.Method) + `<br>
开始时间: ` + htmlEscape(rep.Started.Format("2006-01-02 15:04:05")) + `<br>
生成路径: ` + fmt.Sprint(rep.Total) + ` 条 / 收录: ` + fmt.Sprint(TotalKept(rep)) + ` 条` + fmt.Sprintf(" / 剔除噪音: %d 条", rep.Dropped) + `
</div>
` + strings.Join(sects, "\n") + `
` + allBlock + `
<div class="notice">` + htmlEscape(reportNotice) + `</div>
</body>
</html>
`

	if err := os.WriteFile(outPath, []byte(doc), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "结果已保存: %s（%d 条，html）\n", outPath, TotalKept(rep))
	return nil
}

// SaveMarkdownReport 导出 .md（分类小标题 + GitHub 风格表格 + 说明）
func SaveMarkdownReport(rep ReportData, outPath string) error {
	var sb strings.Builder
	sb.WriteString("# MuLuSaoMiao 扫描报告\n\n")
	sb.WriteString(fmt.Sprintf("- **目标**: %s\n", rep.Target))
	sb.WriteString(fmt.Sprintf("- **请求方法**: %s\n", rep.Method))
	sb.WriteString(fmt.Sprintf("- **开始时间**: %s\n", rep.Started.Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("- **生成路径**: %d 条\n", rep.Total))
	sb.WriteString(fmt.Sprintf("- **收录**: %d 条\n\n", TotalKept(rep)))

	for i, c := range rep.Cats {
		sb.WriteString(fmt.Sprintf("## %d. %s（%s）— %d 条\n\n", i+1, c.Key, c.Title, len(c.Results)))
		sb.WriteString("> " + c.Desc + "\n\n")
		if len(c.Results) == 0 {
			sb.WriteString("（无命中）\n\n")
			continue
		}
		sb.WriteString("| # | 状态 | URL | 大小 | 耗时(ms) | 标题 | 重定向 | Server |\n")
		sb.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for j, r := range c.Results {
			sb.WriteString(fmt.Sprintf("| %d | %d | %s | %s | %d | %s | %s | %s |\n",
				j+1, r.Status, mdCell(r.URL), humanSize(r.Size), r.TimeMs,
				mdCell(r.Title), mdCell(redirectDesc(r)), mdCell(r.Server)))
		}
		// 本分类 URL 列表（便于复制）
		sb.WriteString(fmt.Sprintf("**本类全部 URL（%d）**：\n", len(c.URLs)))
		sb.WriteString("```\n" + strings.Join(c.URLs, "\n") + "\n```\n\n")
	}
	// 全部 URL 汇总（置于说明之前）
	all := AllURLs(rep)
	if len(all) > 0 {
		sb.WriteString(fmt.Sprintf("## 全部 URL 汇总（%d）\n\n", len(all)))
		sb.WriteString("```\n" + strings.Join(all, "\n") + "\n```\n\n")
	}
	sb.WriteString("---\n\n" + reportNotice + "\n")

	if err := os.WriteFile(outPath, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "结果已保存: %s（%d 条，md）\n", outPath, TotalKept(rep))
	return nil
}

// mdCell 表格单元格转义（| 与换行）
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}