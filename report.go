package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	colorReset   = "\033[0m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorRed     = "\033[31m"
	colorMagenta = "\033[35m"
	colorCyan    = "\033[36m"
	colorBlue    = "\033[34m"
)

// statusColor 按状态码着色：
//   2xx 绿（200 绿）· 3xx 黄（重定向）· 401/403 蓝 · 404 品红 · 其他 4xx 品红 · 5xx 红（500 红）
func statusColor(code int) string {
	switch {
	case code >= 200 && code < 300:
		return colorGreen
	case code >= 300 && code < 400:
		return colorYellow
	case code == 401 || code == 403:
		return colorBlue
	case code >= 400 && code < 500:
		return colorMagenta
	default:
		return colorRed
	}
}

// humanSize 人类可读大小
func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// RenderConsole 终端输出扫描结果
func RenderConsole(results []Result, noColor bool) {
	for _, r := range results {
		status := fmt.Sprintf("%d", r.Status)
		if !noColor {
			status = statusColor(r.Status) + status + colorReset
		}
		line := fmt.Sprintf("%-5s %s %s %dms", status, r.URL, humanSize(r.Size), r.TimeMs)
		if len(r.Chain) > 0 {
			// 跟随模式：显示完整重定向链（最终落地）
			parts := make([]string, 0, len(r.Chain))
			for _, hop := range r.Chain {
				parts = append(parts, compactURL(hop))
			}
			line += fmt.Sprintf(" [→ %s]", strings.Join(parts, " → "))
		} else if r.Location != "" {
			line += fmt.Sprintf(" -> %s", r.Location)
		}
		if r.Server != "" {
			line += fmt.Sprintf(" [server: %s]", r.Server)
		}
		if r.Title != "" {
			if noColor {
				line += fmt.Sprintf("  %s", r.Title)
			} else {
				line += fmt.Sprintf("  %s%s%s", colorCyan, r.Title, colorReset)
			}
		}
		fmt.Println(line)
	}
}

// compactURL 压缩完整 URL 为可读路径（同目标时省略协议与主机）
func compactURL(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			rest = rest[j:]
		} else {
			rest = "/"
		}
		if len(rest) > 48 {
			rest = rest[:48] + "…"
		}
		return rest
	}
	return u
}

// SaveReport 按扩展名导出结果：.json / .csv / .html / .md / .xlsx / .docx / .txt
// 所有格式统一输出 6 类有价值结果（分类分组）+ 末尾说明，非 6 类条目不收录
func SaveReport(results []Result, target, outPath string, started time.Time, total int64, method string) error {
	rep := BuildReportData(results, target, started, total, method)
	ext := strings.ToLower(filepath.Ext(outPath))
	switch ext {
	case ".json":
		return SaveJSONReport(rep, outPath)
	case ".csv":
		return SaveCSVReport(rep, outPath)
	case ".html", ".htm":
		return SaveHTMLReport(rep, outPath)
	case ".md", ".markdown":
		return SaveMarkdownReport(rep, outPath)
	case ".xlsx":
		return SaveExcelReport(rep, outPath)
	case ".docx", ".word":
		return SaveDocxReport(rep, outPath)
	}
	return SaveTextReport(rep, outPath)
}

// SaveJSONReport JSON：完整结构 = 元信息 + 分类统计 + 分类明细(含 urls) + 全部 URL 汇总 + 说明
func SaveJSONReport(rep ReportData, outPath string) error {
	summary := map[string]any{}
	for _, c := range rep.Cats {
		summary[c.Key] = map[string]any{"title": c.Title, "count": len(c.Results)}
	}
	payload := map[string]any{
		"tool":        "MuLuSaoMiao",
		"version":     version,
		"target":      rep.Target,
		"method":      rep.Method,
		"started":     rep.Started.Format(time.RFC3339),
		"total_paths": rep.Total,
		"kept":        TotalKept(rep),
		"dropped":     rep.Dropped,
		"summary":     summary,
		"categories":  rep.Cats,
		"all_urls":    AllURLs(rep),
		"notice":      reportNotice,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("结果已保存: %s（%d 条，json）\n", outPath, TotalKept(rep))
	return nil
}

// csvSafe 防 CSV 公式注入：以 = + - @ Tab CR 开头的文本前置单引号。
// title/location 等字段来自被扫目标的响应，Excel/WPS 打开报告时
// 可能把这些单元格当公式执行。
func csvSafe(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// SaveCSVReport CSV：分类表格（每行一条）+ 每类 URL 块 + 全部 URL 块 + 说明行
// URL 块行格式：<category>_urls,url（机器可读，可用 category 列区分）
func SaveCSVReport(rep ReportData, outPath string) error {
	var sb strings.Builder
	w := csv.NewWriter(&sb)
	_ = w.Write([]string{"category", "category_label", "target", "url", "status", "size", "time_ms", "title", "location", "chain", "final_url", "server"})
	n := 0
	for _, c := range rep.Cats {
		for _, r := range c.Results {
			_ = w.Write([]string{c.Key, c.Title, r.Target, r.URL, fmt.Sprint(r.Status), fmt.Sprint(r.Size),
				fmt.Sprint(r.TimeMs), csvSafe(r.Title), csvSafe(r.Location), csvSafe(strings.Join(r.Chain, " -> ")),
				csvSafe(r.FinalURL), csvSafe(r.Server)})
			n++
		}
		// 本分类 URL 块（便于整列复制）
		_ = w.Write([]string{c.Key + "_urls", "本类全部 URL"})
		for _, u := range c.URLs {
			_ = w.Write([]string{c.Key + "_urls", u})
		}
	}
	// 全部 URL 汇总块（置于说明行之前）
	_ = w.Write([]string{"all_urls", "全部 URL 汇总"})
	for _, u := range AllURLs(rep) {
		_ = w.Write([]string{"all_urls", u})
	}
	_ = w.Write([]string{"notice", reportNotice})
	w.Flush()
	if err := os.WriteFile(outPath, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("结果已保存: %s（%d 条，csv）\n", outPath, n)
	return nil
}

// SaveTextReport TXT：分类分块制表符文本 + 每类 URL 列表 + 全部 URL 汇总 + 说明
func SaveTextReport(rep ReportData, outPath string) error {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# MuLuSaoMiao 扫描报告  target=%s  method=%s  收录=%d 条  生成路径=%d\n",
		rep.Target, rep.Method, TotalKept(rep), rep.Total))
	for _, c := range rep.Cats {
		sb.WriteString(fmt.Sprintf("\n[%s] %s（%d 条）\n", c.Key, c.Title, len(c.Results)))
		sb.WriteString("  提示: " + c.Desc + "\n")
		if len(c.Results) == 0 {
			sb.WriteString("  （无命中）\n")
			continue
		}
		for _, r := range c.Results {
			line := fmt.Sprintf("%d\t%s\t%d\t%dms", r.Status, r.URL, r.Size, r.TimeMs)
			if len(r.Chain) > 0 {
				line += "\tchain=" + strings.Join(r.Chain, " -> ")
			} else if r.Location != "" {
				line += "\t-> " + r.Location
			}
			if r.Title != "" {
				line += "\t" + r.Title
			}
			sb.WriteString(line + "\n")
		}
		// 本分类 URL 列表（便于复制）
		sb.WriteString(fmt.Sprintf("  --- %s 全部 URL（%d）---\n", c.Key, len(c.URLs)))
		for _, u := range c.URLs {
			sb.WriteString("  " + u + "\n")
		}
	}
	// 全部 URL 汇总（置于说明之前）
	all := AllURLs(rep)
	sb.WriteString(fmt.Sprintf("\n=== 全部 URL 汇总（%d）===\n", len(all)))
	for _, u := range all {
		sb.WriteString(u + "\n")
	}
	sb.WriteString("\n" + reportNotice + "\n")
	if err := os.WriteFile(outPath, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("结果已保存: %s（%d 条，txt）\n", outPath, TotalKept(rep))
	return nil
}

// printProgress 进度条（stderr，避免污染 stdout 结果流）
func printProgress(done, total int64) {
	pct := float64(done) / float64(total) * 100
	fmt.Fprintf(os.Stderr, "\r[进度] %d/%d (%.1f%%)", done, total, pct)
}

// SharedProgress 多目标并行时的共享进度条：多个 Scanner 副本共用同一条
// "\r" 进度行（避免各自 \r 互相覆盖），totals 由每个目标生成路径后累加。
type SharedProgress struct {
	mu    sync.Mutex
	done  int64
	total int64
}

// AddTotal 累加分母（每个目标生成路径后调用一次）。
func (p *SharedProgress) AddTotal(n int64) {
	p.mu.Lock()
	p.total += n
	p.mu.Unlock()
}

// Tick 完成一个请求后推进；保持与单目标一致的 50 条一刷。加锁防止并行交错。
func (p *SharedProgress) Tick() {
	p.mu.Lock()
	p.done++
	if p.done%50 == 0 {
		printProgress(p.done, p.total)
	}
	p.mu.Unlock()
}

// parsedStatusSet 解析 "200,301,404" → map[int]bool；支持 Nxx/NNx 通配（如 2xx、4xx、40x）；非法项忽略
func parsedStatusSet(s string) map[int]bool {
	out := make(map[int]bool)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if codes, ok := expandStatusToken(part); ok {
			for _, c := range codes {
				out[c] = true
			}
			continue
		}
		var c int
		if _, err := fmt.Sscanf(part, "%d", &c); err == nil && c >= 100 && c <= 599 {
			out[c] = true
		}
	}
	return out
}

// expandStatusToken 展开状态码通配：2xx→200-299，4xx→400-499，40x→400-409
func expandStatusToken(part string) ([]int, bool) {
	if len(part) != 3 || (part[2] != 'x' && part[2] != 'X') {
		return nil, false
	}
	h := part[0]
	if h < '1' || h > '5' {
		return nil, false
	}
	base := int(h-'0') * 100
	if part[1] == 'x' || part[1] == 'X' {
		codes := make([]int, 0, 100)
		for i := base; i < base+100; i++ {
			codes = append(codes, i)
		}
		return codes, true
	}
	if part[1] < '0' || part[1] > '9' {
		return nil, false
	}
	tens := int(part[1]-'0') * 10
	codes := make([]int, 0, 10)
	for i := base + tens; i < base+tens+10; i++ {
		codes = append(codes, i)
	}
	return codes, true
}

// sortedResults 按 URL 排序（导出前统一）
func sortedResults(rs []Result) []Result {
	cp := make([]Result, len(rs))
	copy(cp, rs)
	sort.Slice(cp, func(i, j int) bool { return cp[i].URL < cp[j].URL })
	return cp
}
