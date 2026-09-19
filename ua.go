package main

import (
	"math/rand"
	"strings"
	"sync"
)

// UA 选择模式
type UAMode int

const (
	UAModeFixed   UAMode = iota // 固定 UA（默认 DictScan Chrome）
	UAModeBrowser               // 从浏览器池随机
	UAModeBot                   // 从爬虫池随机
	UAModeRandom                // 全池随机
)

// uaBrowsers 内置常见浏览器 UA（桌面 + 移动端）
var uaBrowsers = []string{
	// Chrome 桌面
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	// Edge
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0",
	// Firefox 桌面
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:127.0) Gecko/20100101 Firefox/127.0",
	"Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0",
	// Safari
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
	// 移动端
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36",
}

// uaBots 常见搜索引擎爬虫与工具 UA
var uaBots = []string{
	"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
	"Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
	"Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/search/spider.html)",
	"Mozilla/5.0 (compatible; YandexBot/3.0; +http://yandex.com/bots)",
	"Mozilla/5.0 (compatible; DuckDuckBot-Https/1.1; https://duckduckgo.com/duckduckbot)",
	"curl/8.6.0",
	"curl/7.88.1",
	"Wget/1.21.4",
	"python-requests/2.31.0",
	"Go-http-client/2.0",
	"Go-http-client/1.1",
	"Scrapy/2.11.0 (+https://scrapy.org)",
	"python-urllib/3.12",
	"Java/21.0.1",
	"PostmanRuntime/7.37.0",
	"Thunder Client (https://www.thunderclient.com)",
	"OhDear/2.0 (https://ohdear.app)",
	"Apache-HttpClient/4.5.14 (Java/17.0.9)",
	"Mozilla/5.0 (compatible; MJ12bot/v1.4.8; http://mj12bot.com/)",
	"Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)",
}

// uaPool 全池（浏览器 + 爬虫，供 random 模式与历史 --random-agent 使用）
var uaPool = append(append([]string{}, uaBrowsers...), uaBots...)

const defaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// ParseUAMode 解析 --ua 值：
// browser/crawler → 浏览器池随机；bot → 爬虫池随机；random/rotate → 全池随机；其他 → 固定 UA
func ParseUAMode(v string) (UAMode, string) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "browser":
		return UAModeBrowser, ""
	case "bot", "crawler":
		return UAModeBot, ""
	case "random", "rotate":
		return UAModeRandom, ""
	}
	return UAModeFixed, v
}

var uaMu sync.Mutex

// userAgent 按配置选择 UA：
//  SessionUA 非空（会话级，每目标固定一个）  → 直接使用
//  --random-agent / --ua random → 全池随机
//  --ua browser                → 浏览器池随机
//  --ua bot                    → 爬虫池随机
//  --ua <字符串>               → 固定该 UA
//  默认                        → 内置 Chrome UA
func (s *Scanner) userAgent() string {
	if s.SessionUA != "" {
		return s.SessionUA
	}
	if s.UAMode == UAModeRandom || s.RandomUA {
		return pickUA(uaPool)
	}
	switch s.UAMode {
	case UAModeBrowser:
		return pickUA(uaBrowsers)
	case UAModeBot:
		return pickUA(uaBots)
	}
	if s.CustomUA != "" {
		return s.CustomUA
	}
	return defaultUA
}

// pickUA 从池中随机取一个（线程安全）
func pickUA(pool []string) string {
	uaMu.Lock()
	defer uaMu.Unlock()
	return pool[rand.Intn(len(pool))]
}