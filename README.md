# MuLuSaoMiao — Go 目录扫描工具

命令行目录扫描工具，核心亮点是**强大的字典管理与路径生成**：多字典组合、关键词动态生成、智能扩展名追加、`{FUZZ}` 模糊测试占位符。全部基于 Go 标准库实现，零第三方依赖，单二进制即可运行。字典通过 `go:embed` 内嵌进二进制，**分发时无需携带任何附加文件**，天然支持跨平台交叉编译。

## 功能一览

| 能力 | 说明 |
| --- | --- |
| 基础扫描 | 内置常见目录/文件名字典，自动生成 `词` 与 `词/` 两种路径 |
| 多字典加载 | `-w` 可多次/逗号分隔加载外部字典；`#` 注释与空行自动忽略 |
| 完全替换内置字典 | `--no-builtin`：不加载内置词表/扩展/域名关键词，路径 100% 由 `-w` 自定义字典决定 |
| 多字典组合 | `--combine prefix/suffix/both` + `-prefix-dict`/`-suffix-dict`，在路径**前缀/后缀位置**插入第二本字典 |
| 关键词动态生成 | `--keyword example.com,wordpress`：自动生成 `example-backup`、`example_dev`、`backup_example`、`wordpress_config` 等针对性变体 |
| 智能扩展名 | `--smart-ext`：对每个词自动追加 `.bak` `.old` `.tar.gz` 等 28 种备份/临时后缀 |
| 模糊测试占位符 | `-fuzz "/api/{FUZZ}/v1"`：支持 `{FUZZ}` `{ext}` `{HOST}` `{PRODUCT}` `{range:1-100}` `{2digit}` |
| 规模护栏 | `--max-paths` 限制单目标生成路径数上限，超限截断并警告（`--smart-ext` × 大字典时先兜底） |
| 高并发扫描 | 默认 50 并发；可调超时、Cookie、UA 池（会话级随机）、自定义请求头、代理、`--tls-verify` 证书校验 |
| 速率控制 | `--rate` 令牌桶限速、`--burst` 突发量、`--jitter` 间隔抖动、`--auto` 自适应并发 |
| 重定向处理 | 默认记录 `Location` 不跟随；`--follow` 跟随并显示完整重定向链 |
| 伪重定向识别 | `--fake-redir`：学习首页/登录页指纹，自动过滤跳转到首页/登录页的伪有效目录 |
| 结果过滤 | `--status` 白名单 / `--hide` 黑名单（支持 `2xx` / `40x` 通配）；彩色状态码、页面标题、响应大小、耗时 |
| 智能响应分析 | `--ac`：先请求随机路径学习"无效页"特征（状态码/长度/内容哈希/标题），自动过滤软 404 假阳性 |
| 多目标 | `-u` 单目标 + `-f` 目标文件（每行一个 URL，`#` 注释）；`--parallel` 并行扫描多个目标 |
| 结果导出 | `-o` 按扩展名导出 JSON / CSV / HTML / Markdown / XLSX / DOCX / TXT 七种格式；统一按 6 类风险分组（200/301·302/401/403/500·502·503/405）只输出有价值结果；可逗号分隔/重复 `-o` 一次导出多份 |
| 被动情报收集 | `--extract` / `--crawl`：从页面链接/HTML 注释/JS/robots.txt/sitemap 提取新路径并自动扫描 |
| **分层模块化字典** | 字典拆分为按**通用性**（core/common/tech/sensitive）与**价值密度**（high/mid/low）分层的可装配模块；`--layers`/`--density` 精确控制装配组合 |
| **指纹自动装配** | `--assemble`：先识别目标技术栈（Server/X-Powered-By/Cookie/generator/页面特征），自动装配匹配的 tech 字典模块（WordPress/ThinkPHP/Laravel/Jenkins/GitLab…） |
| **指纹侦察** | `--fingerprint`：仅识别目标指纹并列出，不扫描 |
| 中断保护 | Ctrl+C 立即取消在途请求并停止扫描，已扫结果照常输出/导出；再次 Ctrl+C 强制退出 |

---

## 快速开始

### 直接用现成二进制（无需编译）

```bash
# Windows
MuLuSaoMiao.exe -u http://example.com --hide 404

# Linux / macOS
./MuLuSaoMiao -u http://example.com --hide 404

# 完整实战：内置字典 + 外部字典 + 智能扩展名 + 关键词变体 + JSON 导出
MuLuSaoMiao.exe -u http://example.com -w big.txt --smart-ext --keyword example.com,wordpress -o result.json
```

### 自己编译（详见下一节"编译与跨平台构建"）

```bash
go build -o MuLuSaoMiao.exe .       # Windows
go build -o MuLuSaoMiao .           # Linux / macOS
```

---

## 参数详解

```
-u <url>          目标 URL，如 http://example.com/（无协议自动补 http://；可配合 -f 批量）
-f <file>         目标文件，每行一个 URL（# 注释）；等效 URL 自动去重合并（/ 尾斜杠、:80/:443 显式端口）
--parallel <n>    并行扫描目标数：n 个目标同时扫描（默认 0 = 自动 min(4, 目标数)；1 = 串行）；每个目标内部仍用 -c 并发
-w <file>         字典文件（可多次 -w 或用逗号分隔多个）；正常为追加，--no-builtin 下为唯一词源
--no-builtin      完全不用内置字典（common/admin/backup/extensions），仅用 -w 词表 + --ext 自定义扩展；自动域名关键词同时关闭，路径完全由自定义字典决定
--combine <模式>  多字典组合：none | prefix | suffix | both（默认 none）
--prefix-dict     前缀字典文件（配合 --combine prefix/both）
--suffix-dict     后缀字典文件（配合 --combine suffix/both）
--smart-ext       智能追加备份/临时后缀（.bak .old .tar.gz ~ 等）
--ext <列表>      自定义扩展名（逗号分隔，覆盖内置；如 bak,old,~；需配合 --smart-ext 或 {ext} 模板生效）
--keyword <列表>  关键词（域名/产品名，逗号分隔），自动生成变体
--fuzz <模板>     模糊测试模板，如 /api/{FUZZ}/v1（可逗号分隔多个）
--max-paths <n>   单目标最大生成路径数（0 = 不限制），超过时截断并警告
-c <n>            并发数（默认 50）
--timeout <秒>    请求超时秒数（默认 5）
-m <方法>         HTTP 方法（默认 GET；支持 GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS 等标准方法或自定义大写 token）
--status <列表>   只显示这些状态码（白名单），支持 2xx/4xx/40x 通配（与 --hide 同传时白名单优先）
--hide <列表>     隐藏这些状态码（黑名单），同样支持通配如 404 / 4xx
--ac              智能响应分析：先请求随机路径学习无效页特征，自动过滤假阳性
--ac-count <n>    校准用随机路径数（默认 5，--ac 时生效）
--follow          跟随重定向并显示重定向链（默认只记录 Location）
--fake-redir      识别并过滤跳转到首页/登录页的伪有效目录
-H "Name: Value" 自定义请求头（可多次 -H，或用逗号分隔多条）
--ua <值>         User-Agent：browser | bot | random，或指定完整 UA 字符串（random 模式每目标固定一个）
--random-agent    随机 User-Agent（等价 --ua random）
--cookie <串>     请求 Cookie，如 session=abc123
--body <数据>     请求体：直接文本或 @文件路径（如 @post.json，配合 -m POST 测试 API）
--content-type    请求体 Content-Type（默认 application/x-www-form-urlencoded）
--proxy <url>     代理：http://127.0.0.1:8080 或 socks5://127.0.0.1:1080（联动 Burp 抓包）
--tls-verify      校验服务器 TLS 证书（默认关闭：跳过自签名/过期证书）
--extract         被动提取：从响应页面链接/HTML 注释/JS/robots.txt/sitemap 提取新路径并自动扫描
--passive         被动提取（--extract 的别名）
--crawl           爬行模式：被动提取 + 以目标首页为起点（无需字典也能爬）
--crawl-max <n>   每目标被动提取路径上限（默认 500）
--depth <n>       递归扫描深度：1 = 仅初始目录；2 = 发现子目录再深入一层…；0 = 不限深度（受硬顶 32 层与 --max-dirs 保护）
--max-dirs <n>    递归时每层最多进入的子目录数（默认 20，0 = 不限）；防止目录体量爆炸
--layers <层>     分层装配字典模块：core,common,tech,sensitive（逗号分隔；默认 core,sensitive 与旧版一致；含 tech 时强制装配全部 tech 模块）
--density <值>    价值密度过滤：high,mid,low（逗号分隔；默认全部。high=后台/备份/配置等高价值命中；仅作用于 tech 模块装配，不影响 core/sensitive 内置层）
--assemble        指纹自动装配：扫描前识别目标技术栈，自动装配匹配的 tech 字典模块（如 WordPress/Jenkins/GitLab）
--fingerprint     仅识别目标指纹并输出（不扫描），供侦察与组合决策
--list-dicts      列出全部内置字典模块（名称/层/密度/词数/指纹关键词）后退出
--rate <rps>      每秒最大请求数（0 = 不限速）
--burst <n>       令牌桶突发容量（默认 = rate）
--jitter <%>      请求间隔抖动百分比 0-100（模拟真实流量防 WAF 指纹）
--max-retries <n> 网络失败重试次数（指数退避：100ms、200ms、400ms…上限 1s）
--max-conns <n>   单目标最大连接数（0 = 不限制）
--auto            自适应并发：按延迟/错误率动态调整
--auto-max <n>    自适应并发上限（默认 = 4×并发数）
-o <file>         输出文件：可多次 -o 或逗号分隔多份（按各文件扩展名 .json/.csv/.html/.md/.xlsx/.docx/其他→txt）
--no-color        禁用彩色输出（管道重定向时自动禁用）
--quiet           安静模式（不显示进度条）
--version         显示版本
```

彩色终端按状态码着色：2xx 绿、3xx 黄、401/403 蓝、404 品红、5xx 红。

---

## 编译与跨平台构建

### 1. 环境要求

- 安装 [Go](https://golang.org/dl/) 1.24+（任意平台均可），`go version` 验证。
- 本项目**全部使用标准库**（net/http、crypto/tls、encoding/json、regexp、embed 等），**不依赖 cgo、无第三方依赖**，因此**不需要目标平台的编译器**——在你自己的机器上就能为 Windows / Linux / macOS / ARM 等任意平台生成可执行文件。
- 唯一的"附加文件" `dicts/*.txt` 已通过 `go:embed` 内嵌进二进制，编译产物**单文件即可运行**。

### 2. 本平台直接编译

```bash
# Windows（cmd / PowerShell）
go build -o MuLuSaoMiao.exe .

# Linux / macOS
go build -o MuLuSaoMiao .
```

### 3. 交叉编译到其他系统

交叉编译只靠两个环境变量：`GOOS`（目标操作系统）和 `GOARCH`（目标 CPU 架构）。构建机语法（三种常见 shell）：

| 构建机 Shell | 写法（示例：编译 Linux amd64） |
| --- | --- |
| **bash / git bash / WSL** | `GOOS=linux GOARCH=amd64 go build -o MuLuSaoMiao-linux-amd64 .` |
| **cmd.exe（Windows）** | `set GOOS=linux && set GOARCH=amd64 && go build -o MuLuSaoMiao-linux-amd64 .` |
| **PowerShell（Windows）** | `$env:GOOS="linux"; $env:GOARCH="amd64"; go build -o MuLuSaoMiao-linux-amd64 .` |

常用 GOARCH：`amd64`(x86-64) / `386`(x86 32位) / `arm64`(ARM 64 位) / `arm`(ARM 32 位)。

> ⚠️ cmd.exe 中 `GOOS=linux go build` 这种写法**不生效**，必须 `set GOOS=...`。编译完可用 `set GOOS=& set GOARCH=` 恢复。

### 4. 优化参数（全部可选）

| 参数 | 说明 |
| --- | --- |
| `CGO_ENABLED=0` | 禁用 CGO，产出**纯静态**二进制，可放进最精简的 docker / alpine / busybox 环境直接跑 |
| `-trimpath` | 去掉构建机路径信息，构建可复现 |
| `-ldflags "-s -w"` | 去掉符号表与调试信息，体积约小 30%（windows/amd64 实测约 10.8 MB → 7.5 MB） |

**推荐生产构建命令**（以 Linux amd64 为例）：

```bash
# bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o MuLuSaoMiao-linux-amd64 .

# cmd.exe
CGO_ENABLED=0 && set GOOS=linux && set GOARCH=amd64 && go build -trimpath -ldflags "-s -w" -o MuLuSaoMiao-linux-amd64 .
```

### 5. 常用平台构建矩阵

| (GOOS, GOARCH) | 产物示例 | 说明 |
| --- | --- | --- |
| `windows/amd64` | `MuLuSaoMiao.exe` | Windows 10/11 x64 |
| `windows/386` | `MuLuSaoMiao-x86.exe` | 老 32 位 Windows |
| `windows/arm64` | `MuLuSaoMiao-arm64.exe` | Windows on ARM（骁龙笔记本等） |
| `linux/amd64` | `MuLuSaoMiao-linux-amd64` | 主流服务器/云主机 |
| `linux/386` | `MuLuSaoMiao-linux-386` | 老 32 位 Linux |
| `linux/arm64` | `MuLuSaoMiao-linux-arm64` | 树莓派 4/5、飞腾、鲲鹏 |
| `linux/arm` | `MuLuSaoMiao-linux-arm` | 树莓派 1-3（armv7） |
| `darwin/amd64` | `MuLuSaoMiao-darwin-amd64` | Intel Mac |
| `darwin/arm64` | `MuLuSaoMiao-darwin-arm64` | Apple Silicon Mac (M1-M4) |

完整列表：`go tool dist list`（包含 freebsd、openbsd、netbsd、solaris、riscv64 等）。

### 6. 一键交叉编译脚本

**bash / Git Bash / WSL（build-all.sh）：**

```bash
#!/usr/bin/env bash
set -e
mkdir -p dist
CGO_ENABLED=0

build() {
  local os=$1 arch=$2 ext=$3
  local out="dist/MuLuSaoMiao-${os}-${arch}${ext}"
  echo "==> ${os}/${arch} -> ${out}"
  GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$out" .
}

build windows amd64 .exe
build windows 386  .exe
build windows arm64 .exe
build linux   amd64
build linux   arm64
build darwin  amd64
build darwin  arm64
echo "构建完成，产物见 dist/"
```

**Windows 批处理（build-all.bat）：**

```bat
@echo off
setlocal
if not exist dist mkdir dist
set CGO_ENABLED=0
call :build windows amd64 .exe
call :build windows 386 .exe
call :build linux amd64
call :build linux arm64
call :build darwin arm64
echo 构建完成，产物见 dist\
goto :eof

:build
set GOOS=%1
set GOARCH=%2
set EXT=%3
set OUT=dist\MuLuSaoMiao-%GOOS%-%GOARCH%%EXT%
echo ==^> %GOOS%/%GOARCH% --^> %OUT%
go build -trimpath -ldflags "-s -w" -o %OUT% .
goto :eof
```

### 7. 验证与分发

```bash
# 查看二进制构建信息（GOOS/GOARCH/ldflags 等）
go version -m MuLuSaoMiao-linux-amd64

# 运行验证
./MuLuSaoMiao-linux-amd64 --version     # 输出 MuLuSaoMiao 1.0.0
./MuLuSaoMiao-linux-amd64 -u http://example.com --hide 404
```

分发时**只要拷贝这一个文件**（字典已内嵌），对端无需安装 Go。

---

## 详细使用教程与示例

### 场景一：第一次目录扫描（最简）

```bash
# 用内置字典扫 /admin /api /wp-admin /config 等常见路径，隐藏 404
MuLuSaoMiao -u http://example.com --hide 404
```

终端输出示例：

```
目标 http://example.com：共生成 20634 条路径
200   http://example.com/  12.3KB  1200ms [server: nginx]  Example Domain
200   http://example.com/admin/  4.1KB  320ms  Admin Panel
301   http://example.com/login -> /login.html
403   http://example.com/api  89B  95ms [server: nginx]
扫描完成：1 目标 / 20634 路径 / 4 结果 / 耗时 6.2s
```

> 默认装载 core（common 通用词）+ sensitive（admin 后台词典 1 万+ 词条）并按目标域名自动生成关键词变体，路径数约 2 万条/目标；想控制规模可加 `--max-paths` 或 `--layers core`。

### 场景 2：隐藏软 404（站点对不存在路径也返回 200）

很多站点默认路由把所有未知路径都返回 `200 + 统一 Not Found 页`（软 404）。直接扫会出现几百条假"200"。用 `--ac`：

```bash
# 先校准"无效页"特征，扫描时自动过滤
MuLuSaoMiao -u http://example.com --ac

# 加大校准样本（默认 5 个随机路径），更稳
MuLuSaoMiao -u http://example.com --ac --ac-count 10
```

简单流程：`--ac` 会先请求 `--ac-count` 个随机不存在的路径（如 `/a3f9d0x1k2q7w`），学习其状态码/响应长度/内容 MD5/标题作为"无效页"特征，扫描时命中这些特征的响应一律剔除，**每目标独立学习**；校准请求的方法与限速和正式扫描保持一致（`-m` / `--rate`）。

### 场景 3：配合外部大字典 + 智能扩展名 + 关键词（实战标配）

```bash
# 大字典走 -w（多个文件用逗号或多个 -w），再加备份扩展名与域名关键词变体
# 注意：--status 白名单与 --hide 黑名单同时给时白名单优先，二者选其一即可
MuLuSaoMiao -u http://example.com -w big.txt,api.txt --smart-ext --keyword example.com,wordpress --status 200,403,401,301,302
```

### 场景 4：多目标批量扫描

```bash
# targets.txt（每行一个 URL，可加注释）
http://a.example.com
http://b.example.com:8080
# http://c.example.com（注释行被跳过）

# 同时扫 2 个目标（并行），每个目标内部 100 并发；结果合并导出一份 batch.json
MuLuSaoMiao.exe -f targets.txt --parallel 2 -c 100 --timeout 8 --hide 404 -o batch.json
```

### 场景 5：模糊测试与 ID 枚举

```bash
# 对 API 各版本路径做模糊
MuLuSaoMiao -u http://example.com -fuzz "/api/{FUZZ}/v1", "/api/v1/{FUZZ}", "/v{range:1-3}/{FUZZ}"

# 枚举数字 ID 资源
MuLuSaoMiao -u http://example.com -fuzz "/user/{2digit}", "/order/{range:1000-2000}"

# 备份文件模糊：对每个词测试常见备份名
MuLuSaoMiao -u http://example.com -fuzz "/{FUZZ}{ext}" --hide 404
```

### 场景 6：慢速、隐蔽扫描（限速 + 抖动 + 自适应）

内网或者有 WAF 的目标，不希望被发现/被封：

```bash
# 每秒最多 20 个请求，突发 10 个，间隔抖动 50%，网络失败重试 3 次
MuLuSaoMiao -u http://example.com -w big.txt --rate 20 --burst 10 --jitter 50 --max-retries 3 -c 10

# 或让工具自适应：延迟高就降并发，慢了就涨
MuLuSaoMiao -u http://example.com -w big.txt --auto --auto-max 200
```

### 场景 7：代理联动 Burp Suite 抓包分析

```bash
# HTTP 代理
MuLuSaoMiao -u http://example.com --proxy http://127.0.0.1:8080

# SOCKS5 代理（如通过 SSH 隧道）
MuLuSaoMiao -u http://example.com --proxy socks5://127.0.0.1:1080
```

### 场景 8：API 测试（自定义方法与 Body）

```bash
# POST + JSON body 扫 API 目录
MuLuSaoMiao -u http://api.example.com -m POST --body '{"user":"admin","pwd":"123456"}' --content-type "application/json" -fuzz "/api/{FUZZ}"

# 用文件作为 body（@file 语法）
MuLuSaoMiao -u http://api.example.com -m PUT --body @payload.json -fuzz "/api/{FUZZ}"
```

### 场景 9：爬行模式 / 被动情报收集

```bash
# 从首页开始爬，一路发现并扫描所有内部链接（无需字典）
MuLuSaoMiao -u http://example.com --crawl

# 常规扫描 + 从响应中挖新路径反哺
MuLuSaoMiao -u http://example.com -w dict.txt --extract

# 上限控制：只挖 300 条
MuLuSaoMiao -u http://example.com --crawl --crawl-max 300
```

### 场景 10：跟随重定向 + 伪重定向过滤

```bash
# 302 → 首页/登录页的"伪有效"结果太多，用指纹学习过滤
MuLuSaoMiao -u http://example.com --follow --fake-redir

# 查看完整重定向链
MuLuSaoMiao -u http://example.com --follow
```

### 场景 11：递归扫描（发现子目录自动深入）

```bash
# 深度 1（默认）：与以前一致，只扫初始目录
MuLuSaoMiao -u http://example.com -w dicts/common.txt

# 深度 2：发现 /admin/ 等子目录后，在子目录内再用同一字典扫一层
MuLuSaoMiao -u http://example.com -w dicts/common.txt --depth 2

# 深度 3 + 限制每层最多进入 10 个子目录（防止无限扩张）
MuLuSaoMiao -u http://example.com -w dicts/common.txt --depth 3 --max-dirs 10
```

工作原理：

- 每一层用同一组字典路径对 `base` 扫描，首层 base 是初始目标；
- 命中结果中**真实存在的子目录**（请求路径以 `/` 结尾的目录，或 3xx 重定向到 `/word/` 的目录）会作为下一层 base 自动进入；
- 报告 6 类状态码（200/301·302/401/403/500·502·503/405）之外的响应不视为目录；
- 同一完整 URL 全局只请求一次、同一目录只进入一次，302 跳回父级/他处的“伪目录”会被护栏拒绝；
- `--depth 0` 不限制深度，但仍受 32 层硬顶、每层 `--max-dirs` 分支数与全局去重保护，保证有限终止；
- 递归期间进度会在各层间自动展开（`[层 N] base：N 条路径 → 发现 N 个子目录`），`--quiet` 可静音。

```bash
# 深度 0（谨慎使用）：无限深浅度，仅靠 max-dirs 与硬顶兜底
MuLuSaoMiao -u http://example.com -w dicts/common.txt --depth 0 --max-dirs 50
```

> 提示：`--depth` 会把同一字典应用到每一层，请求总量 ≈ 首层路径数 × 进入目录数 —— 指数增长很快，建议先用小 `--max-dirs`（默认 20）试水；授权范围内使用。

---

## 字典管理与路径生成

### 1. 多字典组合（--combine）

把主字典的每个词与**前缀/后缀字典**组合，插入位置可控：

```
--combine prefix   →  /backup_admin  /backup_admin/
--combine suffix   →  /admin_old     /admin_old/
--combine both     →  /backup_admin_old  /backup_admin_old/
```

组合分隔符统一为下划线 `_`。主词 = 内置字典 + `-w` 外部字典 + `--keyword` 变体。

### 2. 完全替换内置字典（--no-builtin）

默认内置四件字典模块：`common.txt`（65 个常见目录/文件名）、`admin.txt`（10640 条后台路径）、`backup.txt`（18 个备份关键词）、`extensions.txt`（28 种备份/临时扩展名）。若只想用**自己的字典**扫描，加 `--no-builtin`：

```
# 只用 mywords.txt，一个内置词都不带
MuLuSaoMiao -u http://example.com -w mywords.txt --no-builtin

# 连扩展名也自己定
MuLuSaoMiao -u http://example.com -w mywords.txt --no-builtin --smart-ext --ext php,bak,old
```

`--no-builtin` 同时关闭自动域名关键词（`--keyword` 变体仍显式生效），路径完全由 `-w` 词表 + `--ext` 扩展名决定；未提供 `-w` 时报错提醒。

### 3. 关键词动态生成（--keyword）

输入目标域名/产品名，自动产出针对性字典。`--keyword example.com,wordpress` 生成：

```
example / example.com / www.example.com          # 主名与全名
example-backup / example_dev / example.old      # 分隔符 - _ . 空 × 20 种后缀
backup_example / dev.example / old-example      # 前缀变体
wordpress / wordpress_backup / wordpress-config # 产品名及其 backup/config/admin/test 变体
2024/2025/2026 年份后缀
```

### 4. 智能扩展名（--smart-ext）

内置 28 种备份/临时后缀：`.bak` `.old` `.orig` `.save` `.swp` `.tmp` `.tar.gz` `.zip` `.rar` `.7z` `.gz` `.sql` `.db` `.json` `.yaml` `.yml` `.xml` `.txt` `.log` `.conf` `.config` `.php` `.jsp` `.aspx` `.js` `.inc` `.dist` `.sample`。需要其他后缀（如 `~`，`word~` 为 vim 交换文件）可用 `--ext` 追加或覆盖。

### 5. 模糊测试占位符（-fuzz）

模板中的占位符逐一展开，多个占位符自动做**笛卡尔积**：

| 占位符 | 取值 |
| --- | --- |
| `{FUZZ}` | 主字典全部词（内置 + 外部 + 关键词变体） |
| `{ext}` | 扩展名列表（`.bak`、`.old`、`~`…） |
| `{HOST}` | 目标主机名（去协议/端口/www） |
| `{PRODUCT}` | 产品名（`--keyword` 中的产品） |
| `{range:1-100}` | 数字区间展开 |
| `{2digit}` `{3digit}` | 定宽数字（00-99 / 000-999） |

---

### 6. 分层模块化字典与指纹自动装配

内置字典按**通用性**与**价值密度**分组建模块（`dicts/modules/*.txt`，`go:embed` 内嵌），三条路线装配：

- **默认（与旧版完全一致）**：`core`（common 通用词）+ `sensitive`（admin 后台/backup 备份）+ extensions，行为不变。
- **手动分层** `--layers core,common,tech,sensitive`：精确控制装配哪些层；含 `tech` 时强制装配**全部**技术栈模块。
- **指纹自动** `--assemble`：扫描前先探测目标指纹（`Server` / `X-Powered-By` / Cookie / `<meta generator>` / robots.txt 等），只装配**指纹命中的** tech 模块，输出如：

  ```
  装配 http://127.0.0.1:8900：nginx[nginx] wordpress[wordpress,wp-content,wp-login,wordpress_test_cookie]（新增 40 条）
  ```

  `模块[命中关键词]` 一目了然；未命中指纹的目标不装配任何 tech 模块，避免词典膨胀。

`--density high,mid,low` 按价值密度过滤（high = 后台/备份/配置等高价值命中；**仅作用于 tech 模块装配**，不影响 core/sensitive 内置层；`--assemble --density high` 只装配高价值 tech 模块的路径，控制扫描规模）。`--list-dicts` 列出全部模块（名称/层/密度/词数/关键词）辅助决策。

```bash
# 指纹侦察（不扫描）
MuLuSaoMiao.exe --fingerprint -u http://target.com

# 指纹自动装配 + 只扫高价值
MuLuSaoMiao.exe --assemble --density high -u http://target.com --hide 404

# 手动强制全 tech 模块
MuLuSaoMiao.exe --layers tech -u http://target.com -c 20 --hide 404
```

---

## 高级功能详解

### 高性能并发与速率控制

| 参数 | 说明 |
| --- | --- |
| `-c <n>` | 并发请求数（默认 50） |
| `--rate <rps>` | 每秒最大请求数（0 = 不限速） |
| `--burst <n>` | 令牌桶突发容量（默认 = rate，允许启动时瞬间发 burst 个请求） |
| `--jitter <%>` | 对每次请求间隔加 ±j% 随机抖动（0-100），模拟真实流量防 WAF |
| `--max-retries <n>` | 网络失败/5xx 重试（指数退避 100ms→1s，上限 1s） |
| `--max-conns <n>` | 限制单目标最大并发连接数 |
| `--auto` | 自适应并发：每 2 秒观察窗口，错误率 >20% 或延迟涨 50% → 降并发；延迟低且稳定 → 缓慢提升 |
| `--auto-max <n>` | 自适应并发上限（默认 = 4×(-c)） |

限速器实现为令牌桶（全局 next-grant 队列），任意并发下实际速率都不会超过 RPS；`--auto` 适合慢速目标网或不想自己调并发参数的场景。

### 请求定制与身份模拟（-H / --ua / --random-agent / --cookie / --body / --proxy）

```bash
# 自定义头：认证、伪造来源 IP、Referer 一条不漏
MuLuSaoMiao -u http://example.com -H "Authorization: Bearer eyJ..." -H "X-Forwarded-For: 203.0.113.9" -H "Referer: https://example.com/"

# UA 池（内置 13 浏览器 + 20 爬虫/工具；随机模式每个目标固定一个 UA，会话级不逐请求切换）
MuLuSaoMiao -u http://example.com --ua browser    # Chrome/Edge/Firefox/Safari 随机
MuLuSaoMiao -u http://example.com --ua bot        # Googlebot/Bingbot/curl…随机
MuLuSaoMiao -u http://example.com --ua random     # 全池随机
MuLuSaoMiao -u http://example.com --ua "Mozilla/5.0 (compatible; Googlebot/2.1)"

# 多方法 + Body（API 测试）
MuLuSaoMiao -u http://api.example.com -m POST --body '{"user":"admin"}' --content-type "application/json" -fuzz "/api/{FUZZ}"
MuLuSaoMiao -u http://example.com -m PUT --body @payload.json

# 代理：HTTP/SOCKS5 全支持，联动 Burp Suite 抓包
MuLuSaoMiao -u http://example.com --proxy http://127.0.0.1:8080
MuLuSaoMiao -u http://example.com --proxy socks5://127.0.0.1:1080
```

> 自定义头同样作用于 `--ac` 校准与 `--fake-redir` 指纹学习请求，保证行为一致。注意：`-u` 是目标 URL，自定义 UA 请用 `--ua`。

### 智能响应分析（--ac）

很多站点对所有不存在的路径统一返回 `200 + Not Found 页`（软 404）。`--ac` 扫描前先请求 `--ac-count`（默认 5）个随机路径，自动学习无效页特征并过滤：

```bash
MuLuSaoMiao.exe -u http://example.com --ac
# 特征匹配：状态码命中基准 且（长度容差 ±5%/20B 或 内容MD5 相同或 标题相同）→ 判为失效
```

每个目标独立学习；目标不可达时自动跳过校准，不影响扫描。

### 重定向智能处理（--follow / --fake-redir）

```bash
# 跟随重定向，展示完整链路 [→ /a → /b]，JSON 含 chain/final_url
MuLuSaoMiao.exe -u http://example.com --follow

# 学习首页/登录页指纹，剔除"302 → 首页/登录页"的伪有效结果
MuLuSaoMiao.exe -u http://example.com --follow --fake-redir
# 结束时统计行会显示 `剔除伪重定向 N 条`
```

不跟随的默认模式：每行结果右侧显示首个 `Location`。跟随模式：`[→ /a → /b]` 完整链路并记录最终状态码。

### 被动情报收集（--extract / --passive / --crawl）

从扫描响应中自动挖掘新路径，反哺扫描队列：

- HTML `href/src/action/poster/data-url` 属性
- HTML 注释内的 URL 与 `/路径`
- robots.txt 的 `Allow/Disallow/Sitemap` 行
- sitemap.xml 的 `<loc>` 条目
- JavaScript/JSON 内引号包裹的绝对 URL 与 `/` 开头的路径

规则：只保留同源路径、去掉 query/fragment、过滤图片/字体/视频/安装包等静态资源、自动去重；新路径实时入队继续扫描，直到无新发现或达到 `--crawl-max` 上限。结束统计行显示 `被动发现 N 条`。

```bash
# 爬行模式：从首页起一路爬，无需字典
MuLuSaoMiao.exe -u http://example.com --crawl

# 扫描 + 反向提取
MuLuSaoMiao.exe -u http://example.com -w dict.txt --extract

# 配单行字典只爬
MuLuSaoMiao.exe -u http://example.com --crawl -w mini.txt
```

---

## 结果导出（-o）

按输出文件**扩展名**自动识别格式，支持 7 种；**所有格式统一按 6 类风险分组，只输出有价值结果**，末尾附说明：

| # | 分类 | 含义 |
| --- | --- | --- |
| 1 | `200` | 敏感文件、后台、未授权访问 |
| 2 | `301/302` | 重定向至认证区域 |
| 3 | `401` | 认证可爆破 |
| 4 | `403` | 可尝试绕过 |
| 5 | `500/502/503` | 错误泄露或可触发漏洞 |
| 6 | `405` | 可能开启危险方法 |

> 命中 404 等未列状态码的条目不进入报告（count 计入 `dropped`），完整扫描过程与所有请求详情请在**终端扫描页面**查看——报告末尾会以说明形式提示这一点。

| 扩展名 | 格式 | 内容 |
| --- | --- | --- |
| `.json` | JSON | `{tool,version,target,method,started,total_paths,kept,dropped,summary(6类计数),categories[](含每类 urls),all_urls,notice}`；多目标（`-f`）时每条 result 额外带 `target` 标注归属 |

> **total_paths 语义**：开启 `--depth` 递归后为跨层去重后的累计计划请求数（每层路径数之和），不再只是首层字典条数。
| `.csv` | CSV | 表头含 `category,category_label,target`，每行一条；每类后附 `<key>_urls,url` 行、末尾附 `all_urls,url` 行与 notice 行 |
| `.html` / `.htm` | HTML 报告 | 自包含页面：6 个分类节（标题+处置建议+表格+本类 URL 列表）+ 全部 URL 汇总 + 说明 |
| `.md` / `.markdown` | Markdown | 每类一个 `##` 小节（含提示、表格、本类全部 URL 代码块）+ `## 全部 URL 汇总` + 说明 |
| `.xlsx` | Excel | Sheet1「风险汇总」+ Sheet2「明细」+ Sheet3「URL汇总」（分类/URL 两列，便于整列复制） |
| `.docx` / `.word` | Word | 每类一个小节（标题+提示+表格+本类 URL 列表）+ 全部 URL 汇总 + 末尾说明 |
| 其他（含 `.txt`） | 制表符文本 | 分类分块（`[200] 标题（N 条）` + 提示 + 条目 + `--- 200 全部 URL ---`）+ `=== 全部 URL 汇总 ===` + 说明 |

> **URL 复制区**：每种格式都在每个分类后提供**该分类全部 URL 列表**，并在报告说明前提供**以上所有分类 URL 的总和**（`all_urls`），每行一个 URL，方便直接全选复制（HTML 中为可整块选中的 `<pre>`）。

> **零依赖说明**：xlsx / docx 为 OOXML 格式（ZIP + XML），由程序用标准库 `archive/zip` 手写生成，不引入任何第三方库，交叉编译能力不受影响。

> **多格式一次导出**：`-o` 支持逗号分隔多个文件（或重复 `-o`），扫描只执行一次，结果复用，每份按各自扩展名生成：`-o report.json,report.html,report.xlsx` 一次产出 3 份。

```bash
MuLuSaoMiao.exe -u http://example.com -c 50 --hide 404 -o report.json                      # 结构化：脚本处理
MuLuSaoMiao.exe -u http://example.com -c 50 --hide 404 -o report.html                      # 可视化：浏览器打开
MuLuSaoMiao.exe -u http://example.com -c 50 --hide 404 -o report.xlsx                      # 表格：Excel 筛选/透视
MuLuSaoMiao.exe -u http://example.com -c 50 --hide 404 -o report.docx                      # 文档：直接发给同事
MuLuSaoMiao.exe -u http://example.com -c 50 --hide 404 -o result.json,result.html,result.xlsx  # 一次扫描出 3 份
```

终端彩色输出：2xx 绿 / 3xx 黄 / 401/403 蓝 / 404 品红 / 5xx 红；每行含：状态码、URL、大小、耗时、重定向目标、（跟随 时重定向链）、Server、页面标题。

---

## 内置字典

`dicts/` 目录（go:embed 内嵌，无需随二进制分发），词数为 `--list-dicts` 实测：

- `common.txt` — 65 个常见目录/文件名（admin、api、config、.git/config、.env、wp-admin…）
- `admin.txt` — 10640 条后台/敏感路径字典（admin/administrator/wp-content/…，含多级路径；默认并入主词表，自动剥掉首尾斜杠避免 `//` 双斜杠）
- `backup.txt` — 18 个备份关键词（作后缀组合字典与关键词变体源）
- `extensions.txt` — 28 种备份/临时后缀
- `modules/` — 130 个按技术栈划分的 tech 字典模块（WordPress/Tomcat/Nacos/用友…，`--assemble` 按指纹装配）

---

## 开发与测试

```bash
go build -o MuLuSaoMiao.exe .    # 构建
go vet ./...                 # 静态检查
```

> 说明：本仓库为纯生产代码，未附带单元测试文件；建议自建本地 mock 站点做端到端功能验证（递归、指纹、多目标等场景）。

项目结构说明：

```
main.go           入口、参数解析、流程编排
scanner.go        HTTP 扫描引擎（并发/限速/重定向/被动提取/客户端复用）
recursion.go      递归扫描（--depth/--max-dirs、目录发现与去重护栏）
pathgen.go        路径生成器（字典→候选路径、fuzz 模板展开、--max-paths 截断）
dict.go           字典加载（内置 embed + 外部文件 + 关键词生成 + 分层/指纹装配）
fingerprint.go    技术栈指纹识别（--fingerprint / --assemble）
rate.go           令牌桶限速器（RPS/burst/jitter）
report.go         终端渲染、JSON/CSV/TXT 导出（含 CSV 公式注入防护）
report_data.go    报告统一数据模型（6 类风险分组）
report_markup.go  HTML / Markdown 报告生成
report_excel.go   XLSX 报告生成（OOXML 手写，零依赖）
report_docx.go    DOCX 报告生成（OOXML 手写，零依赖）
crawl.go          被动情报提取（HTML/注释/JS/robots/sitemap）
calibrate.go      智能响应分析（软 404 基准学习）
fake.go           伪重定向过滤（首页/登录页指纹学习）
ua.go             UA 池与会话级 UA 选择
```

---

## FAQ 常见问题

**Q：报错 `没有有效目标`？**
`-u` 和 `-f` 至少提供一个。`-f` 文件里空行/`#` 注释不计；解析不出主机的无效行会告警跳过。

**Q：`-u example.com` 不写协议能扫吗？**
能。无协议目标自动按 `http://` 处理（`example.com:8080/path` 也可以）；显式写了 `https://` 则按 https。

**Q：`--smart-ext` 一次会生成多少请求？**
默认词典下 `--smart-ext` 会把每个词扩展 30 种形式（词、词/、28 种后缀），配 admin 万条词典约 33 万路径——先用 `--max-paths` 兜底或 `--no-builtin -w 小字典` 试水。

**Q：`-w` 能传多个字典吗？**
能。`-w a.txt,b.txt` 或多次 `-w a.txt -w b.txt`。

**Q：收到一堆 200 但全是 Not Found 页？**（软 404）
加 `--ac`（自动学习无效页特征并过滤）。

**Q：结果太多怎么办？**
用 `--status 200,301,302,401,403` 白名单或 `--hide 404` 黑名单；再加 `--ac` / `--fake-redir` 去噪。

**Q：怎么导出给后续脚本用？**
`-o result.json`（含完整结构）或 `-o result.csv`（表格）最合适；给人看的报告用 `-o result.html`（浏览器打开）或 `-o result.docx`（Word）。

**Q：会不会把目标打死/触发告警？**
加 `--rate 20 --burst 5 --jitter 30` 限速并加大抖动；默认并发 50，也可 `-c 5` 调低。

**Q：目标返回非法状态码（如 500/超时）？**
`--max-retries 2` 重试 + `--timeout 10` 加长超时。

---

## 合规提醒

请仅对**你拥有授权**的目标进行扫描。目录枚举可能触发目标告警，扫描前请确认已获书面授权。

## 吐槽
这平台下级目录如果想要上传文件，必须一个个进行上传，没法批量上传累了。建议直接用我编译好的二进制使用好了，剩下的模块目录信息懒得传了...
