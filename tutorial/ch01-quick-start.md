# 第 1 章 快速开始：把工具跑起来

> **目标**：15 分钟内完成编译、用自带示例跑通第一次压测，并读懂启动日志、监控输出和退出过程。本章示例零依赖——不需要 MySQL、Redis，也不需要目标游戏服务器。
>
> **前置**：Go ≥ 1.26（`go.mod` 声明 `go 1.26.0`）；所有命令都在仓库根目录执行。

## 1.1 编译

```bash
go build -o stressbot.exe ./cmd/stressbot
```

只是快速体验的话，后面直接用 `go run ./cmd/stressbot ...` 也可以，不必先产出 exe。

启动日志第一行会打印版本号（`version` 字段），默认是 `dev`。正式出包时可以在编译期注入：

```bash
go build -ldflags "-X main.Version=v1.0.0" -o stressbot.exe ./cmd/stressbot
```

## 1.2 第一次运行：零依赖示例

教程第 1 章示例在 `tutorial/examples/ch01/`，流程里只有一个 `setState` 动作（往每个机器人的状态里写两个值），不碰任何网络：

```
tutorial/examples/ch01/
├── stressbot.toml      # 运行配置（机器人数量、日志、监控等）
├── flow/flow.json      # 流程：每个机器人顺序执行 3 次 SayHello
├── proto/demo.proto    # 占位 proto（加载器要求目录里至少有一个 .proto）
├── scripts/            # 空目录（Lua 脚本目录必须存在，可以为空）
└── adapter/
    └── tcp_demo_codec.json   # 声明式协议配置（本章不用它，但目录里至少要有一份 *_codec.json）
```

四个资源目录为什么一个都不能少：

| 资源 | 缺了会怎样 |
|---|---|
| `adapter/` 里的 `*_codec.json` | 启动时按文件名 `<proto>_<service>_codec.json` 推断"有哪些连接"，一份都没有就报错退出。`errors.json` 可选，缺了只打一条 warn |
| `proto/` 里的 `.proto` | 一个都没有就报错退出 |
| `flow/flow.json` | 读不到或 Schema 校验不过就报错退出 |
| `scripts/` 目录 | 目录本身必须存在（启动时要扫描脚本判断是否用了 Redis 共享状态），里面可以没有文件 |

在仓库根目录执行：

```bash
go run ./cmd/stressbot -config tutorial/examples/ch01/stressbot.toml
```

### 你应该看到什么

控制台日志格式是"时间 级别 代码位置 消息 {字段}"。下面是实测输出，去掉了时间戳、代码位置和每行都有的 `"service": "stressbot"` 字段，路径也做了缩写：

**启动阶段**

```
info  [MAIN] stressbot 启动   {"version": "dev"}
info  [POOL] 协程池已初始化   {"cap": -1, "expiry_duration": 60}
info  [MAIN] 单机模式启动     {"botCount": 3, "concurrent": 3, "flow": "...\\ch01\\flow\\flow.json", "proto": "...\\ch01\\proto", "scripts": "...\\ch01\\scripts", "adapter": "...\\ch01\\adapter"}
info  [DEBUG] pprof 服务启动  {"endpoint": "http://localhost:6060/debug/pprof/"}
info  [PROTOX] 已加载 proto 文件  {"count": 1}
info  [PROTOX] 已索引消息类型     {"count": 2}
warn  [MAIN] 未找到 errors.json 错误码表，错误码将不显示中文描述  {"dir": "...\\ch01\\adapter"}   ← 无碍
info  [MAIN] 运行资源已加载   {"connections": 1, "nodes": 4, "actions": 1, "listens": 0}
info  [MAIN] 监控已启用       {"httpEnabled": false, "apdexThresholdMs": 100}
info  [SCRIPT] 已预编译 Lua 脚本  {"count": 0}
info  [MAIN] Lua 脚本已预编译     {"count": 0}
info  [MAIN] Redis 共享状态未启用（脚本未使用 share）
info  [GNET] 客户端引擎已启动 {"numEventLoop": 32, "readBufferCap": 32768, "writeBufferCap": 32768, "maxConcurrentDials": 512, "maxBodyLen": 16777216}
info  [MANAGER] 开始创建机器人   {"count": 3, "concurrent": 3}
info  [MANAGER] 全部机器人已启动 {"count": 3, "requested": 3}
info  [MANAGER] 定时停止已设定   {"duration": 10}
info  [ROBOT] 启动  {"id": 1140001, "index": 1, "account": "bot_1140001"}
info  [ROBOT] 启动  {"id": 1140000, "index": 0, "account": "bot_1140000"}
info  [ROBOT] 启动  {"id": 1140002, "index": 2, "account": "bot_1140002"}
```

几点说明：

- `[ROBOT] 启动` 由每个机器人自己的协程打印，顺序不固定，也可能出现在 `全部机器人已启动` 之后；
- `numEventLoop` 默认等于本机 CPU 核数，你的机器上数字会不同；
- `duration` 字段以秒为单位输出（`10` 即配置里的 `"10s"`）；
- `[GNET]` 那行前面还会混入一行 gnet 库自己的英文日志 `Starting gnet client with N event loops`，可以忽略。

**运行阶段**：控制台按 `monitor.reportInterval` 打印监控摘要（示例里调成了 1 秒，不写默认 5 秒）：

```
[MONITOR] 1s | goroutines=49 | mem=10.2MB | gc=5 | total=3
[MONITOR] robots: 3运行 0停止 0错误 | conn: 0建立 0失败 0断连 | 0.0/0.0 MB/s
[MONITOR] 动作                       成功   失败   超时  RTT avg  RTT p95   client apdex exec   qps
[MONITOR] SayHello                  3    0    0     --ms     --ms      0ms  0.00    0   3.0
[MONITOR] 2s | goroutines=49 | mem=10.3MB | gc=5 | total=6
...
[MONITOR] SayHello                  9    0    0     --ms     --ms      0ms  0.00    0   3.0
```

**结束阶段**：每个机器人执行完 3 个 `SayHello`（每个动作节点之后默认歇 1000ms，所以约 3 秒）流程就走完了。全部机器人结束后进程自动退出——`duration = "10s"` 是**上限**，不是最短运行时间：

```
info  [CLIENT] 关闭所有连接  {"robot": "bot_1140002", "tcp": 0, "udp": 0}
info  [ROBOT] 已停止  {"id": 1140002, "account": "bot_1140002", "cleanup": "ok"}
...
info  [MAIN] 压测已结束，正在关闭...
info  [MANAGER] 全部机器人已停止  {"count": 0, "cleanup": "ok", "totalRobots": 3, "timeoutRobots": 0, "luaSkipped": 0}
info  [MONITOR] CSV 已导出  {"path": "metrics/metrics_2026_10_11_15_23_27.csv"}
info  [MAIN] 已退出
info  [GNET] 客户端引擎已停止
info  [POOL] 协程池开始关闭  {...}
info  [POOL] 协程池已关闭
```

`[MANAGER] 全部机器人已停止` 里的 `count` 是这次"主动停止"时还在跑的机器人数（这里都已自然结束，所以是 0），`totalRobots` 才是累计清理的机器人总数。

如果在运行中按 **Ctrl+C**（SIGINT）或发 SIGTERM，会看到 `[MAIN] 收到退出请求，正在关闭...`，随后同样走"停止全部机器人 → 导出 CSV → 已退出"，进程退出码为 0。

## 1.3 读懂你刚看到的

**机器人编号**：`[ROBOT] 启动` 里的三个字段由配置算出：`id = startNumber + index`，`account = accountPrefix + id`，`index` 从 0 起。示例配置 `accountPrefix = "bot_"`、`startNumber = 1140000`，所以 index 0/1/2 的机器人分别是 `bot_1140000`/`bot_1140001`/`bot_1140002`。`id`、`index`、`account` 是框架保留状态键（第 3 章）。

**监控摘要第一行**：`1s` 是监控采集器启动以来的时长（不是进程启动以来）；`goroutines`/`mem`/`gc` 是本进程的 Go 运行时数据；`total` 是累计完成的动作数。

**第二行**：`robots` 是运行中/正常停止/异常退出的机器人数；`conn` 是累计建立/建立失败/意外断开的连接数；最后是发送/接收带宽（MB/s）。

**动作表各列**：

| 列 | 含义 |
|---|---|
| 动作 | 动作名（flow.json 里 `actions` 表的 key） |
| 成功 / 失败 / 超时 | 三种结果的累计计数 |
| RTT avg / p95 | 网络往返耗时均值与 P95。`SayHello` 是本地动作没有网络往返，显示 `--ms` |
| client | 非网络部分（构建、编解码等）的客户端平均开销 |
| apdex | 满意度评分，只对有网络往返的动作打分；本地、纯发送、纯监听类动作显示 `0.00` |
| exec | 当前正在执行中的数量（瞬时值） |
| qps | 本次打印区间内的每秒完成数。示例 3 个机器人 × 每秒 1 个动作 = 3.0 |

动作出错时，表格下面还会多一行 `[MONITOR] errors: 动作→[框架 错误名]×次数 首条详情`，`框架` 表示错误码 < 100（工具自身产生），`业务` 表示 ≥ 100（服务器返回）。表格只列出有样本或正在执行的动作。

**日志去哪了**：写到 `log.path` 指定的文件（示例为 `log/tutorial-ch01.log`），文件里每行一条 JSON（`timestamp` 为 UTC 时间、`level`、`caller`、`message` 加业务字段），按 `maxSizeMB` 轮转。控制台看到的是同一批日志的彩色文本版，前提是 `printConsole = true`。注意 `[MONITOR]` 开头的监控摘要直接打到标准输出，**不进日志文件**。

**CSV 在哪**：进程退出时写到**当前工作目录**下的 `metrics/metrics_<年_月_日_时_分_秒>.csv`，目录不存在会自动创建。没有配置 `[monitor]` 段时既不打印监控摘要也不导出 CSV。

**pprof**：配置了 `[pprof] port = 6060`，运行期间浏览器打开 `http://localhost:6060/debug/pprof/` 可以看 Go 运行时画像，同一端口还挂着 `/debug/sched` 等调优端点（第 21 章会用到）。端口被占用时只打一条 `[DEBUG] pprof 服务启动失败` 的 error 日志，压测照常进行——同时跑两个示例时会遇到这种情况。

## 1.4 stressbot.toml 键位速查

单机配置的结构如下。键名大小写敏感、严格校验，写错键名会直接启动失败（见 1.7）。字符串值支持环境变量展开：`${VAR}`（变量未定义则启动报错）、`${VAR:-默认值}`、`$$` 表示字面 `$`。

**顶层**（必须写在所有 `[段]` 之前）

| 键 | 默认 | 说明 |
|---|---|---|
| `daemon` | `false` | `true` 时以守护进程运行（与命令行 `-d` 同效，见 1.5） |

**`[log]`**（整段省略时：写 `log/stressbot.log`、`debug` 级别、同时输出控制台）

| 键 | 段内不写时 | 说明 |
|---|---|---|
| `path` | `log/stressbot.log` | 日志文件路径，相对**当前工作目录** |
| `level` | `debug` | `debug`/`info`/`warn`/`error`，其他取值按 `info` 处理 |
| `printConsole` | `false` | 是否同时输出到控制台。**写了 `[log]` 段却没写这一项，控制台就看不到日志**（监控摘要不受影响） |
| `maxSizeMB` | `500` | 单个日志文件上限，超过轮转 |
| `maxBackups` | `1` | 轮转后保留的旧文件数 |
| `maxAge` | `7` | 旧文件保留天数 |
| `localTime` | `false` | 轮转文件名用本地时间 |
| `compress` | `false` | gzip 压缩旧文件 |
| `weChatToken` | 空 | 企业微信机器人 token，仅在严重错误（panic 级）时推送 |

**`[monitor]`**（整段省略 = 不打印监控摘要、不导出 CSV）

| 键 | 默认 | 说明 |
|---|---|---|
| `apdexThresholdMs` | `100` | Apdex 阈值 T（毫秒），≤ 0 按 100 |
| `timingDetail` | `rtt` | 计时细分：`rtt` / `codec` / `full`。不写或写错会打 warn 并回退 `rtt` |
| `reportInterval` | `5s` | 控制台摘要间隔，Go 时长格式；写错或 ≤ 0 启动报错 |
| `[monitor.http] port` | 不启用 | 开一个 HTTP 端口，`/metrics` 返回实时指标 JSON、`/metrics/summary` 返回文本摘要 |
| `[monitor.sketch] relativeAccuracy` / `maxBins` | `0.01` / `2048` | 延迟分布精度，一般不用改 |

**`[pprof]`**：只有 `port` 一个键，写了段就启用，`port` ≤ 0 时用 6060。

**`[standalone]`**：机器人数量、节奏和初始状态，本章只用到最基础的几个，完整说明见第 12 章。

| 键 | 默认 | 说明 |
|---|---|---|
| `duration` | 空 | 运行上限，Go 时长格式（`"10m"`、`"1h30m"`）；空或 `"0"` = 一直运行直到流程走完或 Ctrl+C；不带单位的 `"10"` 会报错 |
| `[standalone.bot] accountPrefix` | `bot_` | 账号前缀 |
| `[standalone.bot] startNumber` | `1` | 起始编号（写 0 也按 1） |
| `[standalone.bot] totalBots` | `1` | 机器人总数（写 0 也按 1） |
| `[standalone.bot] concurrency` | `0` | 创建期限速：每创建这么多个暂停 1 秒；0 = 不限速 |
| `[standalone.bot] mainService` | **必填** | 主连接服务名（第 3 章） |
| `[standalone.bot] defaultDelayMs` | `0` | 动作节点之间的默认间隔，0 = 1000ms，-1 = 不等，允许 -1 或 0~600000 |
| `[standalone.stateExtra]` | 无 | 注入每个机器人初始状态的字符串键值 |
| `[[standalone.rampUp.stages]]` | 无 | 渐进加压阶段 |

**`[redis]`**（`host` 留空 = 不启用；脚本用了 `require("share")` 时必填）：`host`、`port`（默认 6379）、`username`、`password`、`keyPrefix`（默认 `stressbot`）、`defaultClaimTTL`（默认 `30s`）、`opTimeout`（默认 `2s`），连接池在 `[redis.pool]`：`dialTimeout`（5s）、`readTimeout`（2s）、`writeTimeout`（2s）、`maxOpenConns`、`maxIdleConns`、`connMaxLifetime`（默认不限）。

**`[network]`**（gnet 网络引擎调优，全部留空或 ≤ 0 即用默认）：`readBufferCap`（32768）、`writeBufferCap`（32768）、`numEventLoop`（CPU 核数）、`maxConcurrentDials`（同时处于拨号中的连接上限，512）、`maxBodyLen`（单包 body 上限，16777216）。

> **Windows 用户注意**：配置文件请存成**不带 BOM 的 UTF-8**。带 BOM 的文件会报 `toml: invalid character at start of key: U+00EF 'ï'`。Windows PowerShell 5 的 `Set-Content -Encoding UTF8` 会写入 BOM，用编辑器另存为"UTF-8"（无 BOM）即可。

## 1.5 命令行参数

| 参数 | 默认 | 含义 |
|---|---|---|
| `-config <file>` | `conf/stressbot.toml` | 运行配置文件 |
| `-flow <file>` | `<config 所在目录>/flow/flow.json` | 流程配置文件 |
| `-proto <dir>` | `<config 所在目录>/proto` | proto 目录 |
| `-scripts <dir>` | `<config 所在目录>/scripts` | Lua 脚本目录 |
| `-adapter <dir>` | `<config 所在目录>/adapter` | codec 配置目录（`*_codec.json` + 可选 `errors.json`） |
| `-d` | 关 | 以守护进程运行 |

要点：

- 四个资源参数留空时，默认值以**配置文件所在目录**为基准；显式填写时，相对路径以**当前工作目录**为基准（启动日志 `[MAIN] 单机模式启动` 里打印的是解析后的绝对路径，拿不准就看这一行）。
- 参数写错（例如 `-flows`）会输出 `解析启动参数失败: flag provided but not defined: -flows` 并以退出码 1 结束；本程序不打印用法说明，`-h` 也只会得到 `解析启动参数失败: flag: help requested`，以本表为准。
- `-d` 和配置里的 `daemon = true` 只在 Linux 等类 Unix 系统上有意义：进程会派生一个后台子进程后立即退出。**在 Windows 上**会打印 `[DAEMON] Windows 不支持守护进程模式，进程即将退出。...` 然后直接退出，不会转为前台运行——Windows 下请保持 `daemon = false`、不要加 `-d`，需要常驻时用服务包装器（第 13 章）。

## 1.6 跑仓库自带的真实场景

`conf/flow/` 下有 9 份流程文件：`flow.json` 是默认主场景，另外 8 个按场景命名——`rank`/`rank_alone` 排行赛、`guild` 公会、`brawl` 乱斗、`normal` 基础、`spectator` 观战、`sdc` 大场景、`client` 单向压发送。用 `-flow` 等参数切换场景，无需挪动文件：

```bash
go run ./cmd/stressbot -config conf/stressbot.toml -flow conf/flow/normal.json
```

> **注意**：仓库自带的 `conf/scripts/` 里有脚本（`ranked_*.lua`、`guild_*.lua`）使用了 Redis 共享状态（`share` 模块）。启动时会递归扫描整个脚本目录，只要任何一个脚本里出现 `require("share")`（注释里的不算），就要求 `[redis]` 已配置 `host`——这是硬性检查，与这次选的流程是否真的用到该脚本无关。`conf/stressbot.toml` 默认指向 `127.0.0.1:6379`，Redis 连不上会报 `连接共享状态 Redis 失败` 并退出。另外它还配置了 100 个机器人、20/30/50 三阶段渐进加压，以及 `mainService = "logic"`，需要真实的目标服务器才有意义。

## 1.7 常见启动失败对照

以下报错都在**加载期**出现，进程以退出码 1 结束，不会带病运行。分两类：

**配置文件阶段**（日志尚未初始化，报错直接打在标准错误输出，以 `加载配置失败:` 开头，日志文件里看不到）：

| 报错原文 | 原因与解法 |
|---|---|
| `加载配置失败: 读取配置文件: open ...` | `-config` 路径不对 |
| `加载配置失败: 配置文件 <路径> 包含未知字段: standalone.bot2: strict mode: ...` | 严格模式：键名拼错或放错了段。冒号后列出的就是所有未知键 |
| `加载配置失败: 解析配置文件 <路径>: toml: cannot decode TOML integer into string` | 类型不对，典型是 `stateExtra` 里写了不带引号的数字（它只接受字符串） |
| `加载配置失败: 解析配置文件 <路径>: toml: invalid character at start of key: U+00EF 'ï'` | 文件带 UTF-8 BOM，另存为无 BOM |
| `加载配置失败: standalone.bot.mainService 未配置` | `mainService` 必填（第 3 章讲它的语义） |
| `加载配置失败: standalone.bot.defaultDelayMs 取值无效（-5）：...` | 只允许 -1 或 0~600000 |
| `加载配置失败: standalone.rampUp.stages 各阶段 count 之和 (2) 不等于 standalone.bot.totalBots (3)` | 第 12 章 |
| `加载配置失败: monitor.reportInterval 不是合法时长 "abc": ...` | 时长要写成 `"5s"` 这种格式 |
| `加载配置失败: 展开环境变量: ...` | 用了 `${VAR}` 但环境变量没定义；想要可缺省就写 `${VAR:-}` |

**资源加载阶段**（日志已初始化，报错是一条 `[MAIN] 单机模式运行失败` 的 error 日志，具体原因在 `error` 字段）：

| `error` 字段原文 | 原因与解法 |
|---|---|
| `加载运行资源失败: 推断 codec 映射失败: codec 目录 "..." 下无任何 *_codec.json 文件（未声明任何连接的 codec）` | adapter 目录里没有 `*_codec.json` |
| `加载运行资源失败: 推断 codec 映射失败: codec 文件名 "..." 无法解析为 <proto>_<service>_codec.json` | 文件名不合规，如 `tcp_codec.json` 缺服务名 |
| `加载运行资源失败: 加载 proto 文件失败: 未找到任何 .proto 文件` | proto 目录为空 |
| `加载运行资源失败: 加载流程配置失败: ...` | flow.json 读不到、Schema 校验或条件表达式编译失败 |
| `检查脚本共享状态使用失败: ...`（Windows 下后面是 `The system cannot find the file specified.`） | scripts 目录不存在。**目录必须存在**，没有脚本就放个空目录。在它之前还会先出现一条 warn `[MAIN] Lua 脚本预编译失败（非致命错误）` |
| `流程脚本使用了共享状态 share，但未配置 Redis` | scripts 目录里有脚本用了 `share` 但 `[redis] host` 为空。配置 Redis，或换不含 share 的脚本目录 |
| `连接共享状态 Redis 失败: ...` | 配了 Redis 但连不上 |
| `解析 standalone.duration 失败: time: missing unit in duration "10"` | `duration` 要带单位 |
| `启动网络引擎失败: ...` | gnet 引擎启动失败 |

资源加载的顺序是 codec → proto → flow → Lua 预编译 → duration → Redis 检查 → 网络引擎，所以同时有多处错误时，先报的是排在前面的那一个。

## 1.8 验收

- [ ] `go run ./cmd/stressbot -config tutorial/examples/ch01/stressbot.toml` 一次跑通，约 3 秒后自动退出
- [ ] 能在监控表里找到 `SayHello` 的成功计数（3 → 6 → 9 递增）
- [ ] 知道日志文件、CSV、pprof 分别在哪，以及监控摘要不进日志文件
- [ ] 能说出至少 3 个加载期硬报错的原因，并区分它们出现在标准错误输出还是日志里

## 延伸阅读

- 配置加载的实现细节：[docs/configuration.md](../docs/configuration.md)
- 下一章：[第 2 章 架构总览](ch02-architecture.md)
