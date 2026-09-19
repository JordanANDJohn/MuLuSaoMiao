package main

import "time"

// ReportCategory 一个风险分类及其命中的结果
type ReportCategory struct {
	Key     string   `json:"key"`     // 如 "200"
	Title   string   `json:"title"`   // 如 "敏感文件、后台、未授权访问"
	Desc    string   `json:"desc"`    // 如何评估/处置的提示
	Results []Result `json:"results"`
	URLs    []string `json:"urls"`    // 本分类全部 URL，便于复制
}

// ReportData 报告统一数据模型：目标信息 + 按风险分组的分类结果
type ReportData struct {
	Target  string           `json:"target"`
	Method  string           `json:"method"` // 扫描使用的 HTTP 方法（复现信息）
	Started time.Time        `json:"started"`
	Total   int64            `json:"total_paths"` // 本次扫描生成的全部路径数
	Cats    []ReportCategory `json:"categories"`
	Dropped int              `json:"dropped"` // 非 6 类、未收录的条目数（噪音）
}

// 分类固定顺序：越靠前越值得优先处理
var categoryDefs = []struct {
	key   string
	title string
	desc  string
}{
	{"200", "敏感文件、后台、未授权访问",
		"页面可直接访问，可能是敏感文件、管理后台或未授权接口，建议人工核查内容与登录校验。"},
	{"301/302", "重定向至认证区域",
		"响应重定向至登录/认证页面，通常存在受保护的后台或接口，可结合后续登录或继续枚举。"},
	{"401", "认证可爆破",
		"需要 HTTP 认证（Basic/Digest 等）但未禁止尝试，可用弱口令/字典爆破（注意合规与频率控制）。"},
	{"403", "可尝试绕过",
		"访问被拒绝，可尝试绕过：URL 大小写/编码变体、尾部斜杠、X-Forwarded-For 头、切换 HTTP 方法等。"},
	{"500/502/503", "错误泄露或可触发漏洞",
		"服务端错误响应可能泄露堆栈/路径/框架信息，或存在可触发的异常，建议重点深挖。"},
	{"405", "可能开启危险方法",
		"服务端明确返回 Method Not Allowed，说明接口可能暴露 PUT/DELETE/TRACE 等危险方法，建议用 OPTIONS 验证。"},
}

// reportNotice 报告末尾的说明（第 7 部分）：告诉用户报告只输出有价值信息
const reportNotice = "说明：本报告仅列出上述 6 类有价值的结果（200/301·302/401/403/500·502·503/405），" +
	"已剔除 404 等噪音；完整扫描过程与所有请求详情请在扫描终端查看。"

// classifyCategory 按状态码归类，返回分类 key；非 6 类返回空串（报告不收录）
func classifyCategory(status int) string {
	switch status {
	case 200:
		return "200"
	case 301, 302:
		return "301/302"
	case 401:
		return "401"
	case 403:
		return "403"
	case 500, 502, 503:
		return "500/502/503"
	case 405:
		return "405"
	}
	return ""
}

// BuildReportData 把扫描结果按 6 类分组；不属于任何类的条目（噪音）不进入报告
func BuildReportData(results []Result, target string, started time.Time, total int64, method string) ReportData {
	rep := ReportData{Target: target, Method: method, Started: started, Total: total}
	rep.Cats = make([]ReportCategory, 0, len(categoryDefs))
	for _, d := range categoryDefs {
		rep.Cats = append(rep.Cats, ReportCategory{
			Key:     d.key,
			Title:   d.title,
			Desc:    d.desc,
			Results: []Result{},
			URLs:    []string{},
		})
	}
	idx := make(map[string]int, len(categoryDefs))
	for i, c := range rep.Cats {
		idx[c.Key] = i
	}
	// 每分类内按 URL 去重（保留首次出现顺序）：同一 URL 只输出一条，
	// 避免 7 种格式中 200 等分类重复列出相同 URL
	seen := make(map[string]map[string]bool, len(categoryDefs))
	for _, d := range categoryDefs {
		seen[d.key] = map[string]bool{}
	}
	kept := 0
	for _, r := range results {
		k := classifyCategory(r.Status)
		if k == "" {
			continue
		}
		if seen[k][r.URL] {
			continue
		}
		seen[k][r.URL] = true
		i := idx[k]
		rep.Cats[i].Results = append(rep.Cats[i].Results, r)
		rep.Cats[i].URLs = append(rep.Cats[i].URLs, r.URL)
		kept++
	}
	rep.Dropped = len(results) - kept
	return rep
}

// AllURLs 报告收录的全部 URL（按分类顺序汇总，去重保留顺序）
// 空结果返回空切片而非 nil，保证 json 输出 [] 而不是 null
func AllURLs(rep ReportData) []string {
	out := make([]string, 0, TotalKept(rep))
	seen := map[string]bool{}
	for _, c := range rep.Cats {
		for _, u := range c.URLs {
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	return out
}

// TotalKept 报告收录的条目总数
func TotalKept(rep ReportData) int {
	n := 0
	for _, c := range rep.Cats {
		n += len(c.Results)
	}
	return n
}