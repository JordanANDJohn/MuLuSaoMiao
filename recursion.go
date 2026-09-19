package main

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
)

// recLevel 递归扫描中的一个层级任务
type recLevel struct {
	base string // 本层基础 URL（无尾斜杠）
	d    int    // 层号：1 = 初始目录
}

// maxRecurseLevels 递归深度硬上限：--depth 0（不限）时仍受此约束，防止
// 服务器对任意路径都返回伪目录（如 /a/ 下永远存在 /a/a/）造成的无限深入。
const maxRecurseLevels = 32

// ScanLevels 递归目录扫描：
//   - 每一层用同一组 paths 对 base 扫描（首层为初始目标）；
//   - 从命中结果识别"真实存在的子目录"（报告 6 类状态码；见 dirFromResult），
//     自动作为下一层 base 继续扫描；
//   - depth 控制最多深入几层（1 = 仅初始目录，保持历史行为；0 = 不限深度，但仍受
//     maxRecurseLevels 硬顶 与 每层 maxDirs 分支限制 与 全局去重 保护，不会无限死循环）；
//   - maxDirs 限制每层最多进入的目录数（0 = 不限），防止目录体量爆炸；
//   - 全局 seenURL / seenDir 保证同一完整 URL 只请求一次、同一目录 base 只进入一次。
//
// 返回全部层的结果（保持各层扫描内排序）与累计请求路径数（供统计/报告 total_paths 使用）。
func (s *Scanner) ScanLevels(baseURL string, paths []string, depth, maxDirs int) ([]Result, int) {
	if len(paths) == 0 {
		return nil, 0
	}
	root := strings.TrimRight(baseURL, "/")
	seenURL := make(map[string]bool)
	seenDir := map[string]bool{root: true}

	var all []Result
	total := 0

	layer := []recLevel{{base: root, d: 1}}
	for len(layer) > 0 {
		if s.stopped() { // Ctrl+C：不再进入后续层
			break
		}
		var next []recLevel
		for _, lv := range layer {
			if s.stopped() {
				break
			}
			if lv.d > maxRecurseLevels || (depth > 0 && lv.d > depth) {
				continue
			}
			// 层内路径：全局去重（不同层可能生成同一完整 URL）
			layerPaths := make([]string, 0, len(paths))
			for _, p := range paths {
				full := joinURL(lv.base, p)
				if seenURL[full] {
					continue
				}
				seenURL[full] = true
				layerPaths = append(layerPaths, p)
			}
			if len(layerPaths) == 0 {
				continue
			}
			total += len(layerPaths)
			if !s.Quiet {
				fmt.Fprintf(os.Stderr, "  [层 %d] %s：%d 条路径\n", lv.d, lv.base, len(layerPaths))
			}

			res := s.Scan(lv.base, layerPaths)
			all = append(all, res...)

			// 收集可深入的子目录（仅报告收录的 6 类状态码视为存在）
			var dirs []string
			for _, r := range res {
				if classifyCategory(r.Status) == "" {
					continue
				}
				dir, ok := dirFromResult(r, lv.base)
				if !ok || seenDir[dir] {
					continue
				}
				seenDir[dir] = true
				dirs = append(dirs, dir)
			}
			sort.Strings(dirs)
			found := len(dirs)
			if maxDirs > 0 && len(dirs) > maxDirs {
				dirs = dirs[:maxDirs]
			}
			if !s.Quiet && found > 0 {
				fmt.Fprintf(os.Stderr, "    → 发现 %d 个子目录，深入 %d 个\n", found, len(dirs))
			}
			for _, du := range dirs {
				next = append(next, recLevel{base: du, d: lv.d + 1})
			}
		}
		layer = next
	}
	return all, total
}

// dirFromResult 从单条命中结果识别可深入的子目录，返回其无尾斜杠完整 URL。
//
// 目录判定规则（两条，命中任一即可）：
//  1. 请求路径本身以 / 结尾（字典含 /word/ 形式）且状态存在 → 目录；
//  2. 请求 /word 返回 3xx 且 Location（未跟随）或 FinalURL（跟随）指向
//     「/word/」形式的路径 → 该路径为目录（经典目录重定向）。
//
// 不存在的响应（404/410 等被 classifyCategory 排除）与首页根路径不产生目录。
func dirFromResult(r Result, base string) (string, bool) {
	u, err := url.Parse(r.URL)
	if err != nil {
		return "", false
	}
	p := u.Path
	if p == "" || p == "/" {
		return "", false
	}
	// 规则 1：请求的路径以 / 结尾
	if strings.HasSuffix(p, "/") {
		// 护栏：3xx 且 Location/FinalURL 指向「不包含本目录」的路径（如父级 /、
		// 跳首页/登录页），说明是伪目录（服务器把所有 /x/xxx 都 302 回去），拒绝深入。
		if r.Status >= 300 && r.Status < 400 {
			loc := r.FinalURL
			if loc == "" {
				loc = r.Location
			}
			if loc != "" {
				lu, err := url.Parse(strings.TrimSpace(loc))
				if err == nil && lu.Path != "" && !strings.HasPrefix(lu.Path, p) {
					return "", false
				}
			}
		}
		return strings.TrimRight(r.URL, "/"), true
	}
	// 规则 2：重定向到「自身 + /」
	if r.Status >= 300 && r.Status < 400 {
		loc := r.FinalURL
		if loc == "" {
			loc = r.Location
		}
		if loc == "" {
			return "", false
		}
		lu, err := url.Parse(strings.TrimSpace(loc))
		if err != nil {
			return "", false
		}
		locPath := lu.Path
		if locPath == "" || !strings.HasSuffix(locPath, "/") || !strings.HasPrefix(locPath, p) {
			return "", false
		}
		if lu.IsAbs() {
			return strings.TrimRight(lu.String(), "/"), true
		}
		// 相对 Location：不能简单按 base 拼接。"/xxx/" 开头的是「主机绝对路径」，
		// 与当前 base 的路径无关（服务器 Location: /admin/config/ 就是绝对路径）；
		// 其余（如 "admin/"）按请求 URL 做 RFC 相对解析。
		bu, uerr := url.Parse(base)
		if uerr != nil {
			return "", false
		}
		if strings.HasPrefix(locPath, "/") {
			return strings.TrimRight(bu.Scheme+"://"+bu.Host+locPath, "/"), true
		}
		ref, rerr := url.Parse(strings.TrimSpace(loc))
		if rerr != nil {
			return "", false
		}
		resolved := u.ResolveReference(ref)
		return strings.TrimRight(resolved.String(), "/"), true
	}
	return "", false
}