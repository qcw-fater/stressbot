---
name: backend-review
description: Use when 审查 stressbot 后端 Go 代码、流程执行引擎、错误码体系、声明式 codec、并发安全、接口同步、监控指标或 Admin/Agent 通信相关变更时。
---

# stressbot 后端代码审查技能

## 技术栈

- **语言**：Go 1.26
- **网络引擎**：gnet（事件驱动，非标准 net/http）
- **Lua 运行时**：gopher-lua（LState 池 + 独占模式）
- **日志**：zap + lumberjack
- **协议**：动态 protobuf（反射加载，非代码生成）
- **监控**：原子计数器 + 延迟直方图（16 桶）+ Apdex
- **HTTP**：net/http 标准库（Admin/Agent 通信）

---

## 项目概述

stressbot 是可配置化通用游戏服务器压测工具，核心设计：解耦业务逻辑与框架。所有消息收发、字段填充、随机化、心跳、回调、条件跳转通过 **JSON 流程配置 + 声明式动作** 表达，少量难以通用的行为通过 **Lua 脚本** 实现。一套 `conf/flow/flow.json + conf/scripts/*.lua` 驱动任意带类似协议头的游戏服务器压测。

**运行模式**：
- **单机模式**（agent.enabled=false）：本机直接运行完整启动序列
- **Agent 模式**（agent.enabled=true）：注册到 Admin，接收任务，下载配置执行
- **Admin 模式**：中心调度服务器，管理 Agent、下发任务、聚合指标

## 开发主旨

将旧压测工具从与特定游戏服务器强绑定的专用工具重构为通用工具。以下四条原则是所有审查的最终标尺：

1. **声明式配置覆盖旧硬编码**：旧工具 `Robot/game` 包下的 `OnHandleXXX` 方法都要能通过配置实现。每个节点可配置发送/接收的 proto 类型、填充 C2S 字段、存储 S2C 字段。
2. **Lua 脚本兜底复杂逻辑**：复杂或不太通用的 `OnHandleXXX` 方法通过 Lua 脚本实现。
3. **声明式随机化模拟用户行为**：随机赋值、过滤、map 填充等效果通过 bindings 的 type 字段实现（当前 17 种）。
4. **验证标准**：新工具 + 新流程配置须与旧工具 + 旧流程配置产生相同运行效果。

## 审查原则

- **只读权限，禁止修改代码**：所有改进建议由用户执行
- **按严重程度分级**：🔴 必须修复 / 🟡 建议修复 / 🔵 可选优化
- **每条意见给出具体文件和行号**

---

## 一、错误码体系

### 1.1 ActionError（engine/errors.go）

所有动作执行错误必须使用 `ActionError`，禁止裸 `fmt.Errorf` 直接进入监控路径：

| 字段 | 类型 | 说明 |
|------|------|------|
| `Code` | `errcode.ErrorCode` | 单一错误码：`<100` 框架码，`>=100` 业务/服务端码 |
| `Detail` | `string` | 上下文（service/route/action/elapsed） |
| `cause` | `error`（私有） | 底层错误，支持 `errors.Is` |

**构造函数**：

| 函数 | 用途 |
|------|------|
| `NewActionError(code, detail, ...cause)` | 统一创建结构化错误；框架码、业务码、超时和服务端 headerErr 都走这一入口 |

### 1.2 框架错误码（errcode/codes.go，29 个）

| 范围 | 层级 | 码值 |
|------|------|------|
| 1–6 | 网络层 | `CONN_NOT_FOUND` / `CONN_CLOSED` / `SEND_FAILED` / `RECV_TIMEOUT` / `CONN_DROPPED` / `ACTION_CANCELED` |
| 11–12 | 协议层 | `ENCODE_FAILED` / `PARSE_FAILED` |
| 21–24 | 构建层 | `CREATE_MSG` / `BIND_FIELD` / `SERIALIZE` / `EXEC_FAILED` |
| 31–32 | 监听层 | `LISTEN_TIMEOUT` / `LISTEN_REGISTER` |
| 41–49 | 配置层 | `ADDR_EMPTY` / `URL_EMPTY` / `URL_SCHEME` / `UNKNOWN_PATTERN` / `HTTP_BUILD` / `HTTP_READ_BODY` / `MARSHAL_BODY` / `HTTP_STATUS` / `HEARTBEAT_CONFIG` |
| 51–54 | Lua 层 | `LUA_NOT_INIT` / `LUA_NO_SCRIPT` / `LUA_EXEC_FAILED` / `LUA_SCRIPT_CHECK` |
| 61–62 | 回调层 | `CALLBACK_LUA` / `CALLBACK_PARSE` |

错误聚合只按 `code` 单维度；不再有 `Kind` 字段或 `ErrorKind()` 接口。`codeRegistry` 是框架码唯一真理源，前端通过 `/sbot/api/error-codes` 获取。

### 1.3 审查检查项

| 检查点 | 严重程度 | 说明 |
|--------|---------|------|
| 新增动作错误路径用裸 `fmt.Errorf` | 🔴 | 必须改为 `NewActionError(code, detail, cause...)`，否则 monitor 无法按 code 聚合 |
| 错误码未注册 | 🔴 | 新增框架码须同步：`errcode/codes.go` 常量 + `codeRegistry` + 前端错误码展示/README |
| 错误码跨层占用 | 🔴 | 严格按层分段，不可占用其他层范围 |
| context.Canceled 走 onError | 🔴 | executor 必须在 onError 处理前检查，否则停止时刷屏 |
| TCP 发送回调检查 headerErr | 🟡 | tcpSend 单向消息不应在回调中检查 headerErr |

---

## 二、架构与分层

### 2.1 依赖方向

```
cmd/agent → robot → engine → adapter/protox/state
                     ↘ network (gnet)
                     ↘ script  (Lua 池)
```

| 检查点 | 说明 |
|--------|------|
| 循环依赖 | `engine` 不能 import `robot`；`script` 不能 import `robot`。交叉引用用接口解耦 |
| 层级越界 | `network` 不直接操作 `state.Store`；`engine` 不直接操作 `network.Connection` |
| 接口隔离 | `engine` 通过 `NetSender`（20 方法）和 `ActionHandler`（3 方法）与外部交互 |

### 2.2 接口同步矩阵

修改左侧接口时，右侧所有位置必须同步：

| 接口 | 同步点 |
|------|--------|
| `engine.NetSender` | `robot/netSenderAdapter` + 相关测试；Lua API 调整需同步 `script/api_network.go` |
| `adapter.Adapter` | `adapter/schema_adapter.go` + `codec/` + `CodecResolver` + `conf/adapter/*_codec.json` + 前端适配器类型 |
| `engine.ActionHandler` | `robot/robotActionHandler`（ExecuteAction / ExecuteBoolean / RegisterListen / CooperativeSleep） |
| `errcode.ErrorCode` 常量 | `codeRegistry` 切片 + `GET /sbot/api/error-codes` + 前端错误码展示 |
| `monitor.CodedError` 接口 | `engine.ActionError` 的 `ErrorCode`/`ErrorDetail` + `monitor/collector.go` recordError |

### 2.3 通用性评估

| 检查点 | 评价标准 |
|--------|---------|
| 新代码是否通用 | 只对当前游戏有效的逻辑应放 Lua 脚本层 |
| 配置驱动能力 | 新功能应通过 flow.json 配置驱动，不改 Go 代码 |
| binding type 扩展 | 新增 type 应覆盖一类场景，不是单点解决方案 |
| 框架/业务分离 | engine/network/protox/script/adapter 中不应出现游戏概念（英雄、战斗等） |

---

## 三、engine 层

### 3.1 动作模式（16 种 pattern）

`tcpSend` / `tcpRequest` / `tcpConnect` / `tcpClose` / `tcpListen` / `udpSend` / `udpRequest` / `udpConnect` / `udpClose` / `udpListen` / `tcpHeartbeat` / `udpHeartbeat` / `httpRequest` / `setState` / `clearState` / `lua`

**注意**：`lua` 在 `robot/robotActionHandler.ExecuteAction` 处理，不在 `ActionExecutor.Execute` switch 中；声明式心跳为 Go-only builder，不触碰业务 LState。

新增 pattern 须同步：`engine/flow.go` 常量 + `action.go` Execute switch + 前端 `types/action.ts` + 前端校验/编辑器。

### 3.2 Binding 类型（17 种）

| 类别 | 类型 |
|------|------|
| 取值 | `fixed` / `state` / `stateFirst` |
| 随机 | `stateRandom` / `stateRandomN` / `stateMapKey` / `stateMapValue` / `randomPick` / `randomPickN` / `randomPickMap` / `randomExclude` / `randomInt` / `randomFloat` / `randomBool` / `randomString` |
| 辅助 | `listSize` / `map` |

| 检查点 | 说明 |
|--------|------|
| wrap 支持 | 新增返回单值的 case 必须赋值 `val`（不能 `return`），确保走底部 wrap/path 后处理。返回切片的 case 可以直接 `return` |
| isImplicitRequired | 值缺失应触发跳过的 type 需加入此 switch（当前：stateFirst/stateRandom/stateRandomN/stateMapKey/stateMapValue） |
| JSON 标签一致性 | 新增 struct 字段必须有 `json:"xxx"` 标签，且标签名与 flow.json key 匹配 |

### 3.3 Executor 信号传播（8 种节点类型）

| 信号 | 来源 | 捕获位置 | 效果 |
|------|------|---------|------|
| `errBreak` | break 节点 | `executeLoop` | 跳出循环 |
| `errContinue` | continue 节点 | `executeLoop` | 跳过本次迭代 |
| `errSkip` | `onError.strategy="skip"` | `executeSequence` | 跳过剩余子节点 |
| `errSkip` | 同上 | `executeLoop` / `executeBoolean` / `executeWeighted` | 视为完成 |
| `context.Canceled` | 任务停止 | `executeAction`（onError 前） | 直接向上传播 |

**onError 行为**（executeAction）：

| 字段 | 行为 |
|----|------|
| `ignoreCodes` | 命中 `ActionError.Code` 后 warn 并继续流程；monitor 仍保留失败样本 |
| `handler` | 普通节点调用边，失败后执行错误处理子流程；不写入 `next` |
| `retry.maxRetries` / `retryDelayMs` | 当前 action 的额外重试次数与重试间隔 |
| `strategy="resume"` / 空 | 最终失败后继续流程 |
| `strategy="skip"` | 返回 `errSkip`，由 sequence/loop/boolean/weighted 捕获 |
| `strategy="abort"` | 包装 `ErrExecFailed` 向上传播 |

`context.Canceled`、`context.DeadlineExceeded` 和 `ErrActionCanceled` 必须在 onError 前归一化并直接上抛。

### 3.4 条件表达式

- **内置**：`state:key op value`，运算符 `>= <= != == > <`，支持 `|| && !` 和括号
- **Lua**：`lua:script_name.lua`，返回 0 = true，非 0 = false
- 解析器在 `engine/cond_parser.go`（递归下降：or → and → unary → atom → comparison）

---

## 四、robot 层

### 4.1 NetSender 接口（20 方法）

| 分类 | 方法 |
|------|------|
| 发送 | `TCPSend` / `UDPSend`（返回 `(int, error)`） |
| 请求 | `TCPRequest` / `UDPRequest`（返回 `(body, headerErr uint64, err error)`） |
| 连接 | `ConnectTCP` / `ConnectUDP` / `CloseTCP` / `CloseUDP` |
| 监听 | `GetTCPListenResp` / `GetUDPListenResp` / `EnsureTCPListener` / `EnsureUDPListener` |
| 心跳 | `RegisterTCPHeartbeat` / `RegisterUDPHeartbeat` |
| 密钥 | `GetTCPSecretKey` / `SetTCPSecretKey` / `GetUDPSecretKey` / `SetUDPSecretKey` |
| HTTP | `HTTPRequest`（返回 `(statusCode, body, error)`） |

### 4.2 并发安全

| 检查点 | 说明 |
|--------|------|
| luaMu 互斥 | 所有访问 `L` 的代码必须持有 `luaMu.Lock()`，包括回调、心跳 builder |
| Store 原子性 | 连续 Get+Set 不是原子的，需原子操作用 `Increment` / `IncrementInt64` |
| 协程池 | 后台 goroutine 统一通过 `utils.GetWorkPool().Go()` 启动（自带 recover） |
| Connection 回调 | `SetOnDisconnect` 在其他 goroutine 触发，不应做耗时操作 |

### 4.3 Lua 执行模式

| 模式 | 函数 | 返回值 |
|------|------|--------|
| Action | `luaPool.RunActionScript(L, name)` | `(code, send, recv, timing, err)`；脚本只返回 code，send/recv/timing 由 Context 自动累计 |
| Boolean | `luaPool.RunBooleanScript(L, name)` | `(bool, error)` |
| Callback | `luaPool.RunCallbackScript(L, name, data, proto)` | `(send, recv, timing, err)`；回调函数名 `onMessage` |

---

## 五、network 层

### 5.1 核心结构

| 结构 | 说明 |
|------|------|
| `Connection` | 22 个字段（全私有），含 responseMap / listenResp / listenCh / heartbeat / sendFunc / closeFunc |
| `Client` | 命名连接池：`TCPConn map[string]*Connection` + `UDPConn map[string]*Connection` |
| `Dialer` | 封装 gnet Client + EventServer，注入 sendFunc/closeFunc 到 Connection |
| `Message` | `{RouteKey string, Data []byte, HeaderErr uint64}` |

### 5.2 RequestResponse 错误码映射

| 条件 | 错误码 | 构造方式 |
|------|--------|---------|
| `c == nil` | `ErrConnNotFound` | `NewActionError` |
| `isClose == 1` | `ErrConnClosed` | `NewActionError` |
| Send 失败 | `ErrSendFailed` | `NewActionError`（包装 sendErr） |
| `ctx.Done()` | `ErrConnDropped` | `NewActionError` |
| 超时 | `ErrRecvTimeout` | `NewActionError`（保留 code，monitor 按 ResultTimeout + code 聚合） |

### 5.3 Send 方法

| 条件 | 错误码 |
|------|--------|
| `c == nil` | `ErrConnNotFound` |
| `isClose == 1` | `ErrConnClosed` |
| `sendFunc == nil` | `ErrSendFailed` |
| sendFunc 出错 | `ErrSendFailed`（底层错误记日志但**未包装进 ActionError**，与 RequestResponse 不一致） |

### 5.4 OnTraffic 帧解析流程

```
peek headerSize 字节 → BodyLength() 纯 Go 计算 → 验证 bodyLen（0 < bodyLen ≤ 16MB）
→ 等待完整帧 → 读取 → DecodeTCP/DecodeUDP → OnReceive(routeKey, body, headerErr)
```

### 5.5 审查检查项

| 检查点 | 说明 |
|--------|------|
| 命名统一 | 全部用 `routeKey`（不使用 `responseKey`） |
| OnReceive 分发优先级 | 先检查 responseMap（一发一收），再检查 listenResp（持久推送） |
| headerErr 非零 | 仍路由消息（记 error 日志），不丢弃 |
| 连接生命周期 | `Close()`（主动）不触发 onDisconnect；`onClose()`（被动）触发 onDisconnect + onClosed |

---

## 六、adapter 层

### 6.1 Adapter 接口（9 方法）

| 方法 | 返回值 | 调用路径 |
|------|--------|---------|
| `HeaderSize() int` | 固定头长度 | 声明式 codec，纯 Go |
| `BodyLength(headerData) int` | body 长度 | 声明式 codec，纯 Go |
| `EncodeTCP(route, body, key) []byte` | 编码后包 | `SchemaAdapter` → `codec.SchemaCodec` |
| `EncodeUDP(route, body, key) []byte` | 编码后包 | 同上，支持 UDP 加密偏移 |
| `DecodeTCP(data, key) (routeKey, body, headerErr)` | 解码结果 | `SchemaAdapter` → `codec.SchemaCodec` |
| `DecodeUDP(data, key) (routeKey, body, headerErr)` | 解码结果 | 同上 |
| `ExpectedRouteKey(route) string` | 期望路由键 | 声明式 route 规则 |
| `Close()` | 无 | `SchemaAdapter` 为幂等 no-op |
| `DescribeError(code) string` | 错误描述 | 共享 `errors.json`；未命中返回空字符串 |

### 6.2 生产路径：声明式 codec

生产路径是 `CodecResolver` 按 `"<proto>:<service>"` 解析到每连接一份 `SchemaAdapter`；`SchemaAdapter` 包装 `codec.SchemaCodec`。`conf/adapter/codec.lua` / `error.lua` 仅为 T1 一致性测试 oracle，非生产依赖。

### 6.3 审查检查项

| 检查点 | 说明 |
|--------|------|
| 接口变更同步 | Adapter 新增/改方法须同步：`adapter/schema_adapter.go` + `codec/` + `CodecResolver` + 前端接口规范 + README |
| 热路径零 Lua | `HeaderSize` / `BodyLength` 在 gnet OnTraffic 调用，必须纯 Go、无阻塞 |
| errors.json | 共享错误码描述文件；`<100` 撞框架码加载期硬报错，业务码未命中返回空字符串 |
| 无 fallback | `CodecResolver.Resolve` 未声明返回 nil，由调用方 fail loud，不写自动兼容兜底 |

---

## 七、script 层

### 7.1 Lua 模块（6 模块，共 63 函数）

| 模块 | 函数数 | 文件 |
|------|--------|------|
| `network` | 20 | api_network.go |
| `robot` | 11 | api_robot.go |
| `utils` | 15 | api_utils.go |
| `proto` | 9 | api_proto.go |
| `json` | 2 | api_json.go |
| `log` | 4 | api_log.go |
| `share` | 2 | api_share.go |

### 7.2 审查检查项

| 检查点 | 说明 |
|--------|------|
| 参数变更 | 增减参数时检查所有 `.lua` 脚本并更新 |
| 错误处理 | 用 `L.RaiseError`（pcall 可捕获），不用 `L.Error` |
| 返回值一致性 | 所有分支返回相同数量的值（Lua 栈一致性） |
| __index 模式 | robot（11 方法）和 proto（4 方法）通过 `__index` 支持面向对象调用 |
| 日志 | Lua 脚本用 `require("log")`，不用已废弃的 `utils.log_info/error` |

---

## 八、monitor 层

### 8.1 CodedError 接口（monitor/collector.go）

```go
type CodedError interface {
    error
    ErrorCode() uint64
    ErrorDetail() string
}
```

`engine.ActionError` 实现此接口。recordError 通过 `errors.As` 提取，并按 code 单维聚合。

### 8.2 ErrorEntry 结构

```go
type ErrorEntry struct {
    Code     uint64   `json:"code"`
    CodeName string   `json:"codeName"` // 框架错误名（业务码为空）
    Messages []string `json:"msgs"`     // 最近 3 条 Detail（环形缓冲）
    Count    int64    `json:"count"`
}
```

### 8.3 ActionResult 枚举

`ResultSuccess`(0) / `ResultFailure`(1) / `ResultTimeout`(2) / `ResultCanceled`(3)

超时/取消由调用方分类后传入 `RecordAction`；错误分布只记录 Failure/Timeout 中的 `CodedError`。

### 8.4 MergeSnapshots

按 `code` 合并错误（sum Count，union Messages 去重上限 5）。计数器直接求和。延迟直方图合并后重算百分位。

---

## 九、admin 层

### 9.1 HTTP API（51 个端点）

路由前缀 `/sbot/`。关键分组：

| 分组 | 端点数 | 说明 |
|------|--------|------|
| Agent 上行 | 7 | register / heartbeat / deregister / stress / system / task-done / pending-task |
| 任务管理 | 7 | CRUD + start / stop + config download |
| Agent 管理 | 5 | list / get / delete / shutdown / shutdown-all |
| 指标 | 7 | 聚合 + per-agent（stress + system） |
| 历史归档 | 10 | CRUD + clone / compare / timeseries / agents / config / tags |
| 日志 | 6 | admin + agent 日志查询和文件下载 |
| 基线资源 | 8 | proto / scripts / adapter / flow / config 读取 |
| 错误码 | 1 | `GET /sbot/api/error-codes` |

### 9.2 任务状态机

```
pending → starting → running → stopping → stopped
                                    → failed
```

单例约束：同一时刻只能有一个活跃任务（starting/running/stopping）。Admin 重启后活跃任务自动重置为 `failed`。

### 9.3 Agent 状态

`idle` / `busy` / `unhealthy` / `offline`

健康检查每 5s 运行，超过 `unhealthyAfter`（默认 30s）标记 unhealthy，超过 `offlineAfter`（默认 60s）标记 offline。超过 `purgeAfter`（默认 24h）无任务的离线 Agent 自动清理。

---

## 十、Go 最佳实践

### 10.1 命名

- **包名**：全小写、简洁、无下划线，避免标准库冲突（`strconv` 而非 `str_conv`）
- **不以包名开头**：函数/类型名不以所在包名开头（包名已提供上下文）：`http.Get()` 而非 `http.HTTPGet()`
- **接口命名**：单方法以 `er` 结尾（`io.Reader`、`io.Closer`），多方法用描述性名称（`RoundTripper`）
- **避免内置冲突**：变量名不用 `min`/`max`/`copy`/`new`/`make`/`len`/`error`/`string`/`int` 等
- **Go 1.21+ 内置**：`min`/`max` 已内置，不要重复实现
- **首字母缩写全大写或全小写**：`HTTP`/`TCP`/`URL`/`ID`/`JSON`，不混写（`httpRequest` ✓，`HttpRequest` ✗）
- **错误变量命名**：导出 `ErrXxx`（`ErrTimeout`、`ErrNotFound`），包内 `errXxx`（`errBreak`、`errSkip`）
- **Receiver 命名**：用类型首字母缩写，同一类型所有方法保持一致（`func (s *Server)` 不混用 `srv`/`self`/`this`）
- **布尔返回**：`Has`/`Is`/`Can`/`Should` 前缀（`HasPrefix`、`IsEnabled`）
- **集合类型用单数**：`type Person struct` 而非 `type Persons`；`[]Person` 已表达复数

### 10.2 错误处理

- **禁止忽略带有错误error返回值**，忽略也要 `_ = fn()` + 注释
- **保留错误链**：`fmt.Errorf("上下文: %w", err)`，不用 `fmt.Sprintf` 或字符串拼接
- **哨兵错误比较**：用 `errors.Is(err, ErrXxx)` 而非 `err == ErrXxx`（支持 unwrap 链）
- **提取错误类型**：用 `errors.As(err, &target)` 提取自定义错误类型（如 `ActionError`）
- **动作执行错误**：统一用 `NewActionError`，禁止 `fmt.Errorf`
- **错误信息小写开头**：`fmt.Errorf("connect to %s: %w", addr, err)` 不大写首字母，不加句号
- **哨兵错误定义**：控制流用 `var errBreak = errors.New(...)`，业务错误用 `ActionError` 结构体
- **错误包装 vs 新建**：有底层 error 用 `%w` 包装；纯新建语义用 `%v` 或直接构造
- **defer 中处理错误**：`defer func() { err = fmt.Errorf("close: %w", closer.Close()) }()` 捕获 defer 错误

### 10.3 注释

- **导出标识符必须有 godoc 注释**：以标识符名称开头（`// NewServer 创建并返回...`）
- **包注释**：`package` 声明前，完整句子描述包的用途
- **结构体字段**：每个导出字段行尾或上一行有注释（说明含义和约束）
- **接口方法**：每个方法行尾或上一行有注释
- **注释「做什么」和「为什么」**：函数签名能表达意图的「做什么」可省略，但复杂逻辑、非显而易见的业务含义、算法步骤必须注释清楚
- **注释不用标记**：`// NOTE: xxx` `// TODO: xxx` `// FIXME: xxx` 仅在必要时使用，不在注释中标记作者或日期
- **日志/错误信息中文**，代码注释中英文均可
- **注释与代码同步**：修改逻辑时必须更新对应注释，过时注释比无注释更危险

### 10.4 日志审查

- **日志级别**：`Error` 仅不可恢复错误；`Warn` 可恢复异常；`Info` 关键业务事件（连接/断开/任务启停）；`Debug` 调试细节（默认关闭）
- **结构化字段**：用 `zap.String("key", val)` 等强类型字段，禁止 `fmt.Sprintf` 拼进消息体
- **错误日志必须带 error 字段**：`zap.Error(err)` 或 `zap.String("error", err.Error())`，便于日志检索
- **热路径禁止高频日志**：心跳、帧同步、每帧回调等循环内禁止 `Info`/`Debug` 日志，仅用 `Debug` 且默认不输出
- **上下文充分**：错误日志应包含 service/route/robotID 等定位字段，避免仅输出 "失败" 无上下文
- **日志中文规范**：与错误信息一致，日志面向操作者，使用中文描述（如 "连接建立失败" 而非 "connection failed"）
- **敏感信息**：压测工具面向内部使用，不强制脱敏；但如果日志会暴露到外部系统（如企业微信 webhook），则对应推送内容应避免完整密钥/token

### 10.5 并发

- **所有 goroutine 可终止**：必须通过 `context.Context` 或 `stopCh` 可控退出
- **协程池**：后台 goroutine 通过 `utils.GetWorkPool().Go()` / `GoWithStop()` 启动（自带 recover）
- **Mutex hat 模式**：互斥锁声明在结构体紧邻其保护的字段上方，注释标注保护范围：
  ```go
  type Server struct {
      mu   sync.Mutex // 保护以下 fields
      conn map[string]*Connection
      seq  uint64
  }
  ```
- **热路径用 atomic**：高频计数器用 `atomic.Int64`/`atomic.Uint64`（如 monitor 采集路径），避免锁竞争
- **RWMutex 读多写少**：注册表、配置等读远多于写的场景用 `sync.RWMutex`
- **sync.Map 条件**：读多写少且 key 稳定（如连接池、responseMap），否则用 `map + Mutex`
- **sync.Once 初始化**：全局单次初始化（`OnceValue` / `OnceValues` Go 1.21+），不用 `init()`
- **WaitGroup 模式**：`wg.Add(1)` 在 goroutine 外调用，`wg.Done()` 用 defer 保证执行
- **channel 方向**：函数参数标注方向 `chan<- T`（只写）或 `<-chan T`（只读）
- **循环内 defer**：延迟释放资源（`mu.Lock(); defer mu.Unlock()`），函数级 defer 不影响性能

### 10.6 接口设计

- **接受接口返回结构体**：函数参数用接口类型，返回值用具体类型（`func NewClient(cfg Config) (*Client, error)`）
- **消费者定义接口**：消费方定义自己需要的小接口，不强迫实现方依赖不需要的方法（`engine` 定义 `NetSender`，`robot` 实现）
- **接口要小**：3-5 个方法为宜，超过 10 个考虑拆分。项目当前 `NetSender`（20 方法）是历史遗留，新增接口保持精简
- **接口断言**：编译期验证实现：`var _ NetSender = (*netSenderAdapter)(nil)`
- **nil 接口检查**：接口值为 nil 不等于底层值为 nil。返回错误时返回 `nil, err` 而非返回含 nil 指针的接口

### 10.7 结构体与类型

- **零值可用**：设计类型使零值有意义。`sync.Mutex`/`bytes.Buffer`/`sync.WaitGroup` 零值即可用，无需构造函数
- **值接收者 vs 指针接收者**：
  - 指针接收者：需要修改接收者、结构体较大、需保证一致性时
  - 值接收者：纯值类型（`type ErrorCode uint64`）、不可变小结构体（`type Point struct{X, Y int}`）
  - **同一类型所有方法统一**：不能混用值/指针接收者
- **构造函数**：`NewXxx(params) (*Xxx, error)`，可失败返回 error。简单零值可用类型不需要构造函数
- **避免暴露内部状态**：字段小写（私有），通过方法暴露（Getter）。项目当前 admin/Task 等结构体字段导出是因为 JSON 序列化需要，属合理例外
- **结构体字段顺序**：相关字段分组，锁字段在保护字段上方，嵌入接口/结构体在顶部

### 10.8 切片与集合

- **预分配容量**：已知大小时 `make([]T, 0, len(source))` 或 `make([]T, len(source))`，避免循环 append 触发扩容
- **nil 切片 vs 空切片**：声明用 `var s []T`（nil，JSON 序列化为 `null`），需要空语义用 `[]T{}`
- **append 不覆盖原切片**：`append` 可能返回新底层数组，必须 `s = append(s, elem)`
- **切片截取不复制**：`s[:n]` 共享底层数组，持有子切片会阻止 GC 回收。需要独立副本用 `copy`
- **map 并发不安全**：并发读写必须加锁或用 `sync.Map`
- **map 删除安全**：Go 1.x+ 可以在 range 中 `delete(m, k)`，安全
- **map 零值 nil**：`var m map[string]int` 的 nil map 可读不可写，写入前必须 `make`

### 10.9 Context 使用

- **函数第一个参数**：`func DoSomething(ctx context.Context, arg Arg) error`，不用放在结构体里
- **不要存储 Request Context**：request-scoped context 不要存入结构体字段，随调用栈传递
- **生命周期 Context 可存**：`context.WithCancel` / `context.WithTimeout` 创建的生命周期 context 可存入结构体（如 `Robot.ctx`、`Manager.ctx`）
- **不要传递 nil context**：不确定用什么时用 `context.Background()`
- **context 只传值不传业务**：`context.Value` 仅用于请求级别的跨层元数据（trace ID、auth token），不传业务参数
- **WithTimeout/WithCancel 包裹**：子操作超时/取消不影响父 context，嵌套使用

### 10.10 常量与枚举

- **外部常量显式赋值**：API 响应、配置文件、协议字段等外部可见的常量必须显式赋值（`TaskRunning State = "running"`），不依赖 iota 顺序
- **iota 仅内部使用**：纯内部枚举可用 iota（`ActionResult` 枚举 0-4），但确保不序列化到 JSON 或写入配置
- **枚举类型定义**：`type MyEnum int` + `const ()` 块，不直接用裸常量
- **无枚举时用字符串常量**：有限取值的字符串参数用 `const` 定义（`const PatternTCPRequest = "tcpRequest"`），不硬编码

### 10.11 JSON 与序列化

- **标签风格**：`json:"camelCase"`，与前端 TypeScript 命名一致
- **omitempty 规则**：可选字段加 `omitempty`（指针、切片、map 的零值省略）；必须区分零值和缺失的字段不用
- **可空字段用指针**：`Deadline *time.Time json:"deadline,omitempty"` 区分"未设置"和"零值时间"
- **排除字段**：`json:"-"` 不序列化内部字段（如 `stopCh chan struct{}`）
- **嵌入 vs 内联**：匿名嵌入会以类型名为 key 嵌套序列化；`json:",inline"` 内联平铺（go 1.21+）
- **整数精度**：int64/uint64 超 2^53 精度丢失，Lua 和 JSON 交互时用字符串

### 10.12 资源管理

- **defer Close**：获取资源后立即 `defer closer.Close()`，确保函数退出时释放
- **defer Unlock**：`mu.Lock(); defer mu.Unlock()` 紧跟锁定
- **defer Rollback**：数据库事务 `tx, _ := db.Begin(); defer tx.Rollback()`；Commit 成功后 Rollback 返回 nil（无副作用）
- **defer ticker.Stop**：`ticker := time.NewTicker(d); defer ticker.Stop()` 防止泄漏
- **资源获取顺序 = 释放逆序**：先获 A 再获 B → defer 释 B 再释 A（defer 栈 LIFO）
- **HTTP Body 必须读取并关闭**：`io.Copy(io.Discard, resp.Body); resp.Body.Close()` 否则连接无法复用
- **LState 归还**：`acquire()` 后必须 `defer release()`，否则池耗尽

### 10.13 函数设计

- **命名返回值**：仅在有意义的场景使用（defer 修改返回值、函数体 return {} 较长时），否则省略
- **同层错误处理**：`if err != nil { return err }` 尽早返回，避免深度嵌套
- **布尔参数用 options 或类型**：`EnableCache(true)` 不如 `opt := WithCache()` 或 `mode := CacheMode`
- **参数数量 ≤ 4**：超过用 Option 模式或配置结构体
- **避免 init()**：显式初始化函数优于隐式 `init()`，便于控制顺序和错误处理

### 10.14 工具链

| 工具 | 命令 |
|------|------|
| 格式化 | `go fmt ./...` |
| 静态检查 | `go vet ./...` |
| 测试 | `go test -v -race ./...` |
| 依赖 | `go mod tidy && go mod verify` |

---

## 十一、已知 Bug 模式

| 模式 | 说明 |
|------|------|
| `fmt.Errorf` 用于动作错误 | 必须改为 `NewActionError` |
| `responseKey` 命名残留 | 统一应为 `routeKey`（变量名、参数名、zap 字段名） |
| `ExpectedResponseKey` 残留 | 应为 `ExpectedRouteKey` |
| TCP 发送回调检查 headerErr | tcpSend 是单向消息，回调中不应检查 headerErr |
| context.Canceled 走 onError | executor 必须提前检查，否则任务停止刷屏 |
| 不走协程池的 goroutine | 必须通过 `utils.GetWorkPool().Go()` 启动 |
| LState 未归还 | acquire 后必须 defer release，否则池耗尽 |
| 过时变更注释 | `// ── 签名变更 ──` 等临时注释应在稳定后删除 |
| Send 未包装底层错误 | `Connection.Send` 失败时底层 error 只记日志未包装进 ActionError，与 RequestResponse 不一致 |
| onClose/Close 回调未调用 | `Connection.onClose()` 和 `Close()` 中 `onDisconnect`/`onClosed` 回调检查了 nil 但 if 体为空，回调实际未被调用 |
| DialTCP ctx 未使用 | `Dialer.DialTCP` 接受 `context.Context` 参数但未用于超时/取消 |

---

## 十二、审查报告模板

```
## Backend Review: <变更描述>

### 概要
- 变更范围：<文件数> 个文件
- 涉及包：<engine/robot/network/script/adapter/monitor/errcode/admin/agent/state>
- 风险等级：<低/中/高>

### 一、错误码
- <新增错误路径是否用 ActionError？错误码是否注册？>

### 二、架构
- 通用性：<是否引入游戏特有逻辑？>
- 分层：<是否违反依赖方向？>
- 接口同步：<NetSender/Adapter 变更是否三处同步？>

### 三、🔴 必须修复
1. [文件:行号] 问题 → 建议方式
2. ...

### 四、🟡 建议修复
1. [文件:行号] 问题 → 建议方式
2. ...

### 五、🔵 可选优化
1. [文件:行号] 建议
2. ...

### 六、🟣 代码质量
1. [分类] [文件:行号] 具体问题描述 → 建议方式
2. ...

其中分类为：命名 / 冗余 / 并发 / 注释 / 日志 / 接口 / 结构体 / 切片集合 / Context / 常量枚举 / JSON / 资源管理 / 函数设计

对于第三、四、五、六部分，可交由用户审批，通过后开始进行修改。

### 七、验证
- [ ] `go build ./...`
- [ ] 前端编辑器校验报告无错误
- [ ] 压测 2~5 分钟，日志无异常 error/warn
```

### 用户引用格式

用户通过编号引用审查报告中的条目来批准修改，格式：

```
三.1 三.2 四.1 五.3 六.4 六.8
```

含义：
- **三.1** → 批准第 1 条 🔴 必须修复
- **四.1** → 批准第 1 条 🟡 建议修复
- **五.3** → 批准第 3 条 🔵 可选优化
- **六.4 六.8** → 批准代码质量第 4、8 条（同样是具体可执行的修改项）

收到引用后，只执行被引用的条目，未引用的不动。

---

## 快速审查清单

1. **编译**：`go build ./...`
2. **错误码**：新增错误路径用 ActionError，不用 fmt.Errorf
3. **命名**：无 `responseKey` / `ExpectedResponseKey` 残留
4. **接口同步**：NetSender/Adapter 变更时 adapter + Lua API + 前端同步
5. **分层**：不违反依赖方向，无循环 import
6. **主旨对齐**：新代码通用，游戏特有逻辑放 Lua/配置
7. **wrap 支持**：新增 binding type 赋值 `val` 而非 `return`
8. **并发安全**：luaMu 持锁、goroutine 走协程池、context 可取消
9. **回调调用**：onDisconnect/onClosed 是否实际被调用（非空 if 体）
10. **注释充分**：关键逻辑有「做什么」+「为什么」注释，可读性好
11. **日志规范**：级别恰当、结构化字段、热路径无高频日志、错误带上下文

---

## 动态积累（审查时追加）

此节记录审查过程中发现的实际案例、已知 bug 模式、API 变更等。
每次使用此 skill 审查后端代码后，应将有价值的新发现追加到对应小节。

### 已知 Bug 模式

- **TCP 发送回调不应检查 headerErr**：tcpSend 单向消息，服务端响应通过回调路径分发。框架不应在回调中检查 headerErr
- **context.Canceled 必须提前拦截**：executor.executeAction 中检查必须在 onError 之前，否则停止时刷屏
- **错误聚合只看 code**：不要恢复 Kind/framework/server 维度；前端按 `code < 100` 推导框架/业务标签
- **onClose/Close 回调未调用**：Connection 中 onDisconnect 和 onClosed 回调的 if 体为空，回调实际未执行
- **Send 未包装底层错误**：Connection.Send 失败时底层 error 只记日志未 wrap 进 ActionError，与 RequestResponse 行为不一致

### 已废弃模式追踪

- `fmt.Errorf` 动作错误 → `engine.NewActionError`（服务端 headerErr 也转为业务码 ActionError）
- `responseKey` → `routeKey`（Message.ResponseKey → Message.RouteKey）
- `ExpectedResponseKey` → `ExpectedRouteKey`（Go + Lua `expected_route_key`）
- `go func()` → `utils.GetWorkPool().Go(fn)`
- `// ── 签名变更 ──` 等临时注释 → 稳定后删除

### 最佳实践沉淀

- [错误码注册] 三处同步：`errcode/codes.go` 常量 + `codeRegistry` + 文档
- [接口变更] 检查所有实现方 + Lua API + 前端类型 + README
- [错误码分段] 严格按层分段，不跨层占用
- [错误码聚合] 只按 `code` 单维聚合；`Kind` 已删除，展示层按 `code < 100` 推导框架/业务标签
- [回调验证] 新增回调逻辑时检查 if 体非空，避免"检查了 nil 但没调用"的模式
- [Actor 所有权] 全局时间轮/共享 worker 只能给 Robot owner 入队或置就绪，禁止直接执行涉及业务 LState、嵌套 state 或多步状态事务的动作；`state.Store` 的锁只能消除 Go data race，不能保证「消费事件→更新多字段→发包」的顺序语义。
- [异步发送所有权] 交给 gnet `AsyncWrite` 的最终 packet 在异步写完成前必须保持不可变；仅当 Adapter 明确把 body 拷贝进独立 packet 时，模板的 body scratch 才可在 Encode 返回后复用。
- [剖面归因] CPU pprof 的 cumulative 占比包含完整子树，不能当作可消除收益；跨 Lua 脚本共享调用栈时先加脚本/action 标签或做等价微基准，再为单项优化设收益目标。

---

## 验证流程

代码修改后必须按以下步骤验证，迭代直到全部通过：

1. **编译**：`go build ./...`
2. **配置校验**：前端编辑器打开 flow.json → 查看校验报告，确保无错误
3. **运行测试**：删除旧日志 → `go run ./cmd/agent -config conf/config.json` → 运行 2~5 分钟
4. **日志审查**：
   - 错误检查：`grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"` 应无输出
   - 业务循环检查：按当前 flow 的关键动作确认已至少完成 2 轮
5. **清理**：确认无误后删除日志，停止进程
