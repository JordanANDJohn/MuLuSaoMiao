package main

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// RateLimiter 令牌桶限速器：控制每秒请求数（RPS），burst 提供启动突发。
//
// 实现采用"全局下一个发放时刻"（next grant time）：
//   - 每个请求在锁内依次领取一个递增的发放时刻 grant；
//   - 请求按各自的 grant 等待，因此任意并发下实际速率都不会超 RPS；
//   - burst 通过把初始 grant 提前 (burst-1)×interval 实现（启动可瞬间放行 burst 个请求）；
//   - jitter 对每个请求的间隔施加 ±j% 随机抖动，模拟真实流量，防 WAF 指纹。
//
// rps <= 0 表示不限速（NewRateLimiter 返回 nil）。
type RateLimiter struct {
	mu sync.Mutex

	interval time.Duration // 两个令牌间的最小间隔 = 1/rate
	burst    int           // 突发令牌数（仅用于初始提前量）
	next     time.Time     // 下一个可发放时刻（可为过去，表示已积累配额）
	jitter   float64       // 0..1 间隔抖动比例
}

// NewRateLimiter 构造限速器。rps <= 0 返回 nil（不限速）。
// burst <= 0 时默认 = rps（取整，至少 1）；jitter 钳制在 [0, 1]。
func NewRateLimiter(rps float64, burst int, jitter float64) *RateLimiter {
	if rps <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = int(rps + 0.5)
	}
	if burst < 1 {
		burst = 1
	}
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	now := time.Now()
	return &RateLimiter{
		interval: time.Duration(float64(time.Second) / rps),
		jitter:   jitter,
		burst:    burst,
		// 初始提前 (burst-1) 个间隔，允许启动即突发 burst 个请求
		next: now.Add(-time.Duration(burst-1) * time.Duration(float64(time.Second)/rps)),
	}
}

// Wait 阻塞直到可以发起下一次请求；返回实际等待时长（0 = 立即放行）。
// 等价于 WaitCtx(context.Background())。
func (rl *RateLimiter) Wait() time.Duration {
	return rl.WaitCtx(context.Background())
}

// WaitCtx 阻塞直到可以发起下一次请求，或 ctx 被取消。
// 返回实际等待时长；0 = 立即放行；-1 = 等待期间被取消（未发放）。
// Ctrl+C 时扫描器调用 WaitCtx 立即可被中断，避免卡在限速等待上。
func (rl *RateLimiter) WaitCtx(ctx context.Context) time.Duration {
	rl.mu.Lock()
	now := time.Now()

	grant := rl.next
	if grant.Before(now) {
		grant = now
	}

	step := rl.interval
	if rl.jitter > 0 {
		f := 1 + rl.jitter*(rand.Float64()*2-1)
		if f < 0.05 {
			f = 0.05
		}
		step = time.Duration(float64(rl.interval) * f)
	}
	rl.next = grant.Add(step)

	rl.mu.Unlock()

	wait := grant.Sub(now)
	if wait > 0 {
		select {
		case <-ctx.Done():
			return -1
		case <-time.After(wait):
		}
	}
	return wait
}

// SetRate 运行时调整速率（自适应限速会用到）。rps <= 0 忽略。
func (rl *RateLimiter) SetRate(rps float64) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rps <= 0 {
		return
	}
	rl.interval = time.Duration(float64(time.Second) / rps)
}