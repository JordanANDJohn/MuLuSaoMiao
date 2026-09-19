package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
)

const randPathChars = "abcdefghijklmnopqrstuvwxyz0123456789"

// Baseline 无效页面（404/无效响应）基准特征，由若干随机路径的响应学习而来。
type Baseline struct {
	StatusCodes map[int]struct{}  // 基准状态码集合
	Lengths     []int64           // 基准响应体长度（容差匹配）
	Hashes      map[string]struct{} // 基准响应体 MD5 集合
	Titles      map[string]struct{} // 基准页面标题集合
	Count       int                // 学习样本数
	Target      string             // 学习目标（展示用）
}

// NewBaseline 创建空基准
func NewBaseline(target string) *Baseline {
	return &Baseline{
		StatusCodes: make(map[int]struct{}),
		Hashes:      make(map[string]struct{}),
		Titles:      make(map[string]struct{}),
		Target:      target,
	}
}

// RandomPath 生成一个几乎不可能存在的随机路径（24-31 位随机串，半概率带扩展名）
func RandomPath(rng *rand.Rand) string {
	var sb strings.Builder
	n := 24 + rng.Intn(8)
	for i := 0; i < n; i++ {
		sb.WriteByte(randPathChars[rng.Intn(len(randPathChars))])
	}
	if rng.Intn(2) == 0 {
		sb.WriteString([]string{".html", ".php", ".json", ".txt", ""}[rng.Intn(5)])
	}
	return "/" + sb.String()
}

// LearnBaseline 对目标请求 count 个随机路径，学习"无效页面"响应特征。
// 请求方法与限速与正式扫描保持一致（-m / --rate），保证学到的特征匹配扫描请求。
// 全部请求失败时返回 nil（目标不可达，不启用过滤）。
func LearnBaseline(s *Scanner, baseURL string, count int, rng *rand.Rand) *Baseline {
	b := NewBaseline(strings.TrimRight(baseURL, "/"))
	client := s.httpClient(false) // 校准关注"未跟随"下的原始响应，不跟随
	ok := 0

	for i := 0; i < count; i++ {
		if s.stopped() { // 中断：停止校准
			break
		}
		path := RandomPath(rng)
		u := joinURL(baseURL, path)

		if s.Rate != nil && s.Rate.WaitCtx(s.ctx()) < 0 { // 与扫描一致走限速
			break
		}
		req, err := http.NewRequestWithContext(s.ctx(), s.Method, u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", s.userAgent())
		if s.Cookie != "" {
			req.Header.Set("Cookie", s.Cookie)
		}
		for name, val := range s.Headers {
			req.Header.Set(name, val)
		}
		req.Header.Set("Accept", "*/*")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()

		b.StatusCodes[resp.StatusCode] = struct{}{}
		b.Lengths = append(b.Lengths, int64(len(body)))
		if len(body) > 0 {
			b.Hashes[hashBody(body)] = struct{}{}
		}
		if t := extractTitle(string(body)); t != "" {
			b.Titles[t] = struct{}{}
		}
		ok++
	}

	if ok == 0 {
		return nil // 目标不可达，无法学习
	}
	b.Count = ok
	return b
}

// Match 判断某条响应是否属于"无效页面"。
// 判定顺序：状态码命中 →（长度容差命中 或 内容哈希命中 或 标题命中）即视为无效。
func (b *Baseline) Match(r Result) bool {
	if _, ok := b.StatusCodes[r.Status]; !ok {
		return false // 状态码不在基准内 → 不是无效页面
	}
	for _, l := range b.Lengths {
		if approxEqual(r.Size, l) {
			return true
		}
	}
	if r.Hash != "" {
		if _, ok := b.Hashes[r.Hash]; ok {
			return true
		}
	}
	if r.Title != "" {
		if _, ok := b.Titles[r.Title]; ok {
			return true
		}
	}
	return false
}

// approxEqual 长度容差匹配：允许偏差 max(20B, 5% × 两者较大值)，适应动态 token 页面
func approxEqual(a, b int64) bool {
	if a == b {
		return true
	}
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	base := b
	if a > b {
		base = a
	}
	tol := int64(20)
	if p := int64(float64(base) * 0.05); p > tol {
		tol = p
	}
	return diff <= tol
}

// Summary 返回校准摘要（状态码集合、长度范围、样本数）
func (b *Baseline) Summary() string {
	if b == nil {
		return "校准失败（目标不可达）"
	}
	var codes []string
	for c := range b.StatusCodes {
		codes = append(codes, fmt.Sprintf("%d", c))
	}
	minL, maxL := int64(-1), int64(-1)
	for _, l := range b.Lengths {
		if minL == -1 || l < minL {
			minL = l
		}
		if l > maxL {
			maxL = l
		}
	}
	return fmt.Sprintf("%d 个随机路径 → 状态码 [%s] 长度 [%d-%d] 哈希 %d 个",
		b.Count, strings.Join(codes, ","), minL, maxL, len(b.Hashes))
}

// hashBody 计算 body MD5
func hashBody(body []byte) string {
	sum := md5.Sum(body)
	return hex.EncodeToString(sum[:])
}