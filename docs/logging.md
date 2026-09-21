# 日志系统（internal/stresslog）

> 本文对照实际代码编写，反映已实施状态（核对基准日 2026-09-20）。文内所有 `文件:行号` 均指向当前源码。

## 1. 设计决策

stressbot 不内置日志的存储与查询子系统：

- **不设进程内日志环形缓冲**，**不提供日志 HTTP API**，**前端无日志面板**——Admin 也不提供
  日志查询、代理或下载端点；
- Admin / Agent / 单机三个进程只负责把结构化日志写到**本地 JSON Lines 文件**（zap + lumberjack
  轮转），控制台可选同步输出；
- 生产环境由**外部采集器统一收集**（参考部署见 §7），查询界面交给 Grafana 等观测工具。

理由：压测会产生海量日志，进程内再建一份缓存/查询通路既占内存又与生产观测体系重复；
文件 + 标准采集链路（Vector/Filebeat → Loki/ClickHouse/ES → Grafana/Kibana）是运维通用方案，
工具侧只保证日志是可被无损解析的结构化 JSON。

## 2. 配置

`stresslog.Config`（`internal/stresslog/logger.go:33-44`），对应各 TOML 的 `[log]` 段：

| 字段 | TOML key | 语义 |
| ---- | ---- | ---- |
| `Path` | `path` | 日志文件路径（相对进程工作目录） |
| `PrintConsole` | `printConsole` | 是否同时输出到控制台（stdout） |
| `LogLevel` | `level` | `debug` / `info` / `warn` / `error`；**其余取值回退 info**（logger.go:318-332） |
| `MaxSize` | `maxSizeMB` | 单文件大小上限（MB），触发轮转；缺省 500（logger.go:98-100） |
| `MaxBackups` | `maxBackups` | 保留的旧文件数；缺省 1（logger.go:101-103） |
| `MaxAge` | `maxAge` | 旧文件保留天数；缺省 7（logger.go:104-106） |
| `LocalTime` | `localTime` | 轮转文件名用本地时间（默认 UTC） |
| `Compress` | `compress` | gzip 压缩轮转出的旧文件 |
| `WeChatToken` | `weChatToken` | 企业微信 webhook 密钥（告警，见 §6） |

完整默认值见 `defaultConfig()`（logger.go:78-89）：`path=log/stressbot.log`、`printConsole=true`、
`level=debug`、`maxSizeMB=500`、`maxBackups=1`、`maxAge=7`、`localTime=true`、`compress=true`；
`InitLog` 的 `buildLogLevel` 参数可强制覆盖级别（当前调用方均传空串，cmd 各 main 调用点见
`cmd/stressbot/main.go:104`）。三份样例：`conf/stressbot.toml:22`（`log/stressbot.log`）、
`conf/agent.toml:21`（`log/agent.log`）、`conf/admin.toml:153`（`log/admin.log`）。

## 3. 输出与格式

### 3.1 文件：一行一个 JSON 对象

文件 sink 用 zap JSON 编码器，稳定字段（`fileEncoderConfig()`，logger.go:133-147）：

| 字段 | 来源 | 说明 |
| ---- | ---- | ---- |
| `timestamp` | 编码器 | **UTC RFC3339Nano**（logger.go:129-131） |
| `level` | 编码器 | 小写级别名 |
| `message` | 编码器 | 日志消息 |
| `caller` | `zap.AddCaller()` | 调用位置，**只保留文件名**（去掉目录路径，logger.go:114-127） |
| `stacktrace` | `zap.AddStacktrace(DPanicLevel)` | 仅 DPanic 及以上级别（logger.go:188） |
| `service` | `zap.Fields` | 进程名（`stressbot` / `agent` / `admin`，logger.go:196） |

业务字段以普通键追加在同一对象内。测试守护：`TestFileLogUsesStableJSONSchema`
（logger_test.go:15-60）校验稳定字段存在、timestamp 可按 RFC3339Nano 解析且为 UTC。

### 3.2 控制台

`printConsole=true` 时追加 console 编码器：彩色小写级别 + 布局时间
`2006/01/02 15:04:05.000000Z0700`（logger.go:149-154、181-184）。控制台 sink 包一层
`noSyncWriter`——zap 的 `Sync()` 不会对 stdout 调 fsync（Windows 控制台/管道常返回无效句柄），
文件 sink 不受影响（logger.go:70-76）。

## 4. 刷新与进程生命周期

- **有界缓冲**：文件写入经 `zapcore.BufferedWriteSyncer`，**256 KiB 缓冲、每秒刷新一次**
  （`fileBufferSize`/`fileFlushPeriod`，logger.go:16-19、171-175）——普通日志批量落盘，
  崩溃至多丢 1 秒窗口；
- **正常退出显式落盘**：`InitLog` 返回幂等关闭函数（logger.go:156-158、205-208），三个进程的
  main 都在 defer 中调用（如 `cmd/stressbot/main.go:67-71`）；关闭会 flush 缓冲并关闭轮转文件
  （`fileLogSink.Close`，logger.go:60-68），重复调用幂等（logger_test.go:92-106）；
- **高严重级别立即刷新**：`Error` / `DPanic` 系列在打日志后显式 `syncFile()`（logger.go:241-251），
  由 `TestErrorAndDPanicLogsFlushImmediately` 端到端守护（logger_test.go:132-165）；
- **Fatal 退出前落盘**：`Fatal` / `FatalF` 的落盘行为由 `TestFatalLogsFlushBeforeExit` 守护——
  子进程真实触发 Fatal 并退出后校验文件非空（logger_test.go:167-203）；
- **顶层 panic**：各 main 的 `run` 在 deferred recover 中先 `stresslog.Error` 落盘 panic 与堆栈、
  再关闭日志，退出码 2（`cmd/stressbot/main.go:59-72`）；
- **重复初始化**：`InitLog` 用原子指针交换 sink，旧 sink 立即关闭，不泄漏轮转句柄
  （logger.go:31、201-203）。

## 5. 日志 API

包级函数直接使用全局 logger（logger.go:233-299）：

- 结构化：`Debug / Info / Warn / Error / DPanic / Fatal`（`...zap.Field`）；
- 松散键值：`DebugS / InfoS / WarnS / ErrorS / DPanicS`；printf 模板：`DebugF / InfoF / WarnF / ErrorF / DPanicF / FatalF`；
- 级别控制：`SetLogLevel` 原子调整、对文件与控制台同时生效（logger.go:301-305）；
  `DebugEnabled` / `LevelEnabled` 供热路径判定（logger.go:307-316）。

**未初始化安全**：全局 logger 零值是 no-op 而非 nil——`InitLog` 之前（配置加载失败场景）任何
日志调用只被丢弃，不会 nil panic（logger.go:21-31；`TestUninitializedLoggerIsNoOpNotNil`，
logger_test.go:113-130）。

## 6. 字段约定（防内建键撞名）

zap 的 JSON 编码器**不去重**：业务字段一旦与内建键同名，一行 JSON 出现两个同名键，解析器
取后者、内建值被静默吃掉。因此：

- 业务日志字段名不得使用 `message` / `level` / `timestamp` / `caller` / `logger` / `stacktrace`；
- **机器人标识用 `robot`**（engine、network 各包同款约定），**协程提交位置用 `submitter`**；
- 全仓守护测试 `TestNoZapFieldsCollideWithEncoderKeys` 扫描所有 `.go` 源码中
  `zap.<T>("保留键", ...)` 形态的调用并失败（`internal/stresslog/reserved_keys_test.go:18-85`）；
  保留键集合直接取自 `fileEncoderConfig()`，编码器配置变更时自动跟随（reserved_keys_test.go:17-28）。

## 7. 告警：企业微信 webhook

配置 `log.weChatToken` 后，**DPanic 及以上级别**自动推送企业微信（logger.go:190-195 的 zap
hook；hook 在 core 写入路径内同步执行）。推送内容为 Markdown：消息、时间、调用文件/行/函数、
Go 版本与 GOOS/GOARCH、本机 IP、进程信息（用户/启动时间/工作目录/主机名）与堆栈
（`internal/stresslog/hook.go:59-76`）。webhook 请求 5 秒超时、失败静默丢弃，token 未配置时
直接跳过（`internal/stresslog/alarm.go:12-14、50-74`）。

## 8. 外部采集参考部署

仓库自带一套本地演示观测栈（`docker-compose.yml:45-90`）：**Vector 采集 `log/agent.log` →
Loki 存储 → Grafana 查询**。要点：

- Vector 配置 `conf/deploy/vector/vector-agent.toml`：file source 只采集 agent.log，remap 解析 zap
  JSON 为结构化字段并按 `timestamp` 归一时间后写 Loki；data_dir 必须落在 Docker named volume
  （挂 Windows 宿主目录会因磁盘缓冲 ENODATA 崩溃循环）；
- Loki / Grafana 配置在 `conf/deploy/loki/` 与 `conf/deploy/grafana/provisioning/`；
- 其它部署形态（Filebeat → Kafka → ClickHouse/ES、Kibana 查询等）同理：采集器按 §3.1 的稳定
  JSON 字段解析即可，无需工具侧配合。

日常排查则直接看本地文件：`log/<进程名>.log`，按 `service` 字段区分进程，`level` 过滤严重度，
配合 §4 的落盘语义，进程异常退出前的最后一批高严重级别日志一定在文件里。
