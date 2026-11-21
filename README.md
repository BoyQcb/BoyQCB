
# dytt_parser_go — M3U8 广告检测/剔除后台（Go 版）

> 当前版本：`1.2.0`（版本号定义于 `internal/core/types.go` 的 `Version`，前端取自 `/admin/api/me`）

单二进制后台服务：解析资源站 m3u8 播放列表，识别并剔除广告片段，输出净化后的播放列表供播放器/采集器直连；
同时提供站点规则管理、播放域名发现、片段人工标记与规则学习等后台能力。

- 语言/运行时：Go 1.26+，仅用标准库 `net/http`，**无任何第三方 Go 依赖**
- 部署形态：单文件可执行程序，静态资源 `//go:embed` 内嵌，跨平台可编译（Windows / Linux）
- 默认监听 `:8091`，默认后台密码 `admin123`
- 当前内置规则站点 9 个：dytt、xg、lz、fengbao、modujx、wsyzym、sdzy、wujin、yyzy

## 功能一览

| 模块 | 说明 |
| --- | --- |
| 对外接口 | `GET /api/clean?url=<m3u8地址>` → `code/msg/origin/link/url/Time`，直接剔除无 dry-run |
| 控制面板 | 今日解析量/成功数/成功率/活跃站点、近 7 天趋势、站点占比、系统概况、最近解析记录 |
| 资源站规则 | 站点列表 + 新增/编辑弹窗（名称、检测模式、URL 匹配前缀/正则多值、缓存子目录、特征码多组、索引模式、码率选择、固定子索引、启停、排序、备注），含新增站点探测向导 |
| 调用日志 | 今日运行日志尾部查看、缓存一键清理 |
| 域名发现 | 对接苹果 CMS 采集接口，按全库均匀跳页批量扫描播放域名；历史库累积、域名族泛化正则、非播放域名黑名单过滤，可一键把域名生成/追加为站点规则 |
| M3U8 解析测试 | 左播放器 + 右片段列表；支持整片（过滤后/原始）与单片段播放、命中区间定位、片段筛选/搜索、原始文本对比、片段人工标记与规则学习；**切换页面即清空解析记录** |
| 系统设置 | 后台入口路径、缓存策略与自动清理、CORS、IP 白名单开关、对外接口地址（自定义接口）等集中配置 |
| 代理池 | 订阅地址拉取代理后**直接入池**（不再逐条探测），按连续失败阈值自动剔除；解析时优先走池内代理，代理不可用自动回退直连 |

## 广告检测模式（站点 `detect_mode`）

| 模式 | 判据 | 适用形态 |
| --- | --- | --- |
| `sequence` | 时长指纹序列在片段序列上滑动匹配（容差 `match_tolerance`） | 广告时长固定且与正片不同（dytt / xg / wsyzym 等） |
| `blockcut` | 按 `DISCONTINUITY` 切块，孤立短块判定为广告（要求时长众数占比 ≥ 50%） | 定长分片、广告为独立块 |
| `slicebatch` | 按切片批次（ts 文件 mtime / ETag 前缀）聚类，孤立单块批次为广告 | 广告与正片同域同目录、时长也相同（sdzy 等） |
| `shortblock` | 孤立超短块（片数少、总时长短）+ 周期性出现 | 分片变长、正片块很长而广告块极短（yyzy 等） |

## 目录结构

```
output/
├── Boyqcb.exe              # 单二进制（含内嵌静态资源，Windows 构建产物）
├── main.go                 # 入口：Web 服务 / CLI 模式
├── go.mod
├── internal/
│   ├── config/             # 配置读取与持久化（data/rules.json）、预置采集接口
│   ├── core/               # m3u8 解析 / 各检测模式 / 剔除 / 缓存 / 学习
│   ├── store/              # 记录 records、标记 marks、域名 discovered 持久化
│   └── web/                # 路由、会话、后台 API、仪表盘、域名发现
├── static/                 # 前端源码（已内嵌，改动后必须重新 go build）
│   ├── index.html
│   ├── css/app.css
│   └── js/app.js, js/echarts.min.js, js/hls.<版本>.js
├── data/                   # rules.json / records.json / seghash.json / marks.json / discovered.json
├── cache/                  # 净化后 m3u8：cache/<站点缓存子目录>/<MD5>.m3u8
└── logs/                   # 按日日志：logs/YYYY-MM-DD.log
```

## 快速开始

```powershell
.\Boyqcb.exe                 # 默认 :8091，启动自动打开浏览器
.\Boyqcb.exe -addr :9000 -nobrowser
go build -o Boyqcb.exe .      # 改完前端/后端后重新构建
```

后台地址 `http://127.0.0.1:8091/`，默认密码 `admin123`（首次登录后请修改）。

> 注意：静态资源已内嵌进二进制，**修改 `static/` 后必须 `go build` 并重启进程**才生效；
> 运行中 exe 被占用无法覆盖，需先结束进程再构建/替换。浏览器端请 Ctrl+F5 硬刷新。

### CLI 模式

```powershell
Boyqcb.exe -cmd=version
Boyqcb.exe -cmd=clean -url "http://xxx/index.m3u8"
Boyqcb.exe -cmd=clear-cache -clear=all      # all / expired / days=N
```

## 接口一览

| 接口 | 鉴权 | 说明 |
| --- | --- | --- |
| `GET /api/clean?url=` | 否 | 剔除广告并缓存，返回净化地址 |
| `POST /api/login`、`/api/logout`、`GET /api/me` | 否/是 | 登录（`{"password":"..."}`）、登出、会话状态 |
| `GET /api/dashboard` | 是 | 控制面板统计 |
| `GET/POST /api/sites`、`PUT/DELETE /api/sites/{id}` | 是 | 站点规则列表与增删改 |
| `POST /api/test` | 是 | 解析测试（返回片段、命中区间、预览 token） |
| `GET /api/test/playlist`、`/api/test/segment`、`/api/test/ts` | 是 | 预览播放列表 / 单段 m3u8 / ts 代理（token 30 分钟有效） |
| `POST/GET/DELETE /api/test/marks` | 是 | 按影片读写/清除人工标记 |
| `POST /api/learn/suggest`、`/api/learn/hash`、`/api/learn/apply` | 是 | 依据标记生成时长指纹/哈希组并写回规则（写回前自动备份 rules.json） |
| `POST /api/discover`、`/api/discover/batch`、`/api/discover/resolve` | 是 | 采集接口域名扫描与解析 |
| `GET/DELETE /api/discover/history` | 是 | 域名历史库查看 / 按接口清除 |
| `GET /api/sources`、`/api/records`、`/api/logs`、`/api/cache`、`POST /api/password`、`/api/probe`、`/api/marks` | 是 | 采集源、解析记录、日志、缓存清理、改密、探测、标记库 |
| `GET/POST /api/settings` | 是 | 全局运行设置读写（缓存、抓取、哈希、代理池等） |
| `GET /api/system/settings` | 是 | 系统设置页聚合数据（后台入口、缓存策略、IP 规则、自定义接口、代理池状态） |
| `GET/POST /api/clean-paths`、`DELETE /api/clean-paths/{id}`、`POST /api/clean-paths/test` | 是 | 对外解析接口地址管理（内置 `/api/clean` 不可删除） |
| `POST/DELETE /api/custom-apis`、`/api/custom-apis/{id}`、`POST /api/custom-apis/test` | 是 | 自定义接口模板管理（便于外部系统按自身路径调用解析） |
| `GET/POST /api/ip-access`、`GET/POST /api/ip-rules`、`DELETE /api/ip-rules/{id}`、`POST /api/ip-rules/check` | 是 | IP 白名单开关、规则增删改与命中校验 |
| `GET /api/proxy-pool`、`POST /api/proxy-pool/refresh`、`/api/proxy-pool/test`、`/api/proxy-pool/remove` | 是 | 代理池状态、拉取入池、连通性测试、移除节点 |

## 处理流程

1. 解析主列表：识别主索引/子索引（如 `index.m3u8` → `mixed.m3u8`），定位片段列表与基础目录；
2. 解析片段：`#EXTINF` 时长、TS 地址、`#EXT-X-DISCONTINUITY` 边界；
3. 广告检测：按站点 `detect_mode` 执行（见上表），`use_hash_check=true` 时再用哈希库做内容确认；
4. 剔除与重写：删除广告段及紧邻边界，TS 地址绝对化；
5. 落盘缓存：原子写入 `cache/<子目录>/<MD5(地址)>.m3u8`，按 `cache_ttl` 过期。

## 数据文件

| 文件 | 用途 |
| --- | --- |
| `data/rules.json` | 站点规则、全局设置、广告哈希库、密码哈希（改动前自动 `.bak_*` 备份） |
| `data/records.json` | 解析记录（控制面板统计） |
| `data/seghash.json` | 段哈希库 |
| `data/marks.json` | 人工标记（按站点+地址归档，供规则学习） |
| `data/discovered.json` | 域名发现历史库（按采集接口累积） |

## 配置要点（data/rules.json）

- `settings.cache_ttl` 缓存秒数（默认 604800）、`match_tolerance` 指纹容差（0.05）、`use_hash_check`、`ads_hash_sample_size`、`fetch_timeout`
- `settings.cache_scheme` 缓存访问协议：`auto`（默认，跟随本次访问协议：HTTPS 访问出 HTTPS、HTTP 访问出 HTTP；若前置反代透传 `X-Forwarded-Proto` 则以透传值为准）/ `https` / `http` 强制
- `sites[]`：`name`、`detect_mode`、`url_prefixes[]` / `url_regexes[]`（多值）、`cache_subdir`、`fingerprints[]`、`index_mode`、`stream_pick`、`fixed_sub`、`filter_regex[]`、`enabled`、`sort`、`remark`
- 一个站点一条规则（同一站点的多个播放域名写进 `url_prefixes`），新增镜像只加规则不改代码

## 代理池（proxy_pool）

- 在系统设置里填入订阅地址（或手动节点）并启用，点「拉取」即把返回的代理**直接写入池**，不做入池连通性探测；
- 解析/扫描时按池内节点轮换取用；节点连续失败达到阈值自动剔除，空池或取用失败自动回退直连，不影响主流程；
- 池内列表可单条移除，也可一键清空；「测试」按钮用于验证单个节点连通性（仅测试用途，不影响入池）。
- 配置项：`settings.proxy_pool_enable`、`proxy_pool_api`、`proxy_pool_timeout`、失败阈值等（见 `data/rules.json`）。

## 安全与运维

- 登录后请立刻改密码；`/api/clean` 免鉴权，限制来源请在反向代理层做白名单。
- 改规则前务必确认无 `dytt_parser_*` 进程在跑，否则内存快照回写会覆盖离线修改；写盘后回读校验。
- 定期清理过期缓存：`-cmd=clear-cache`。

## 规则学习闭环

1. 测试页解析一条影片 → 人工标记漏判（广告判成正片）/ 误判（正片判成广告）/ 疑似片段；
2. 点「学习」查看统计、时长指纹、哈希组与重复命中；
3. 勾选需要的指纹写回站点规则（自动备份 `rules.json`）；
4. 重新解析同一影片验证剔除效果。


