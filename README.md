# stressbot — 通用游戏服务器压测工具

`stressbot` 是可配置化通用游戏服务器压测工具，解耦业务逻辑与框架核心：
所有消息发送/接收、字段填充、随机化、心跳、回调、条件跳转都通过 **JSON 流程配置 + 声明式动作** 表达，
少量难以通用的行为通过 **Lua 脚本** 实现。

一套 `conf/flow/flow.json + conf/scripts/*.lua` 即可驱动任意带类似协议头的游戏服务器压测。

---

## 目录结构

```
stressbot/
├── cmd/
│   ├── agent/            主程序入口（单机模式 / Agent 模式）
│   └── web/              前端可视化编辑器（React + Vite）
├── adapter/              协议适配器接口 + 声明式 codec 引擎（CodecResolver / SchemaAdapter + codec/ Go 编解码、帧分割、错误码映射）
├── errcode/              统一错误码定义（框架码 < 100 + 业务码 ≥ 100，单一 code 维度）
├── admin/                Admin 服务器（分布式调度、历史归档、前端托管）
├── agent/                Agent 节点（注册到 Admin、执行下发任务）
├── engine/               流程执行引擎（节点图遍历、动作模式、字段绑定）
├── logview/              日志环形缓冲区（实时查询 API）
├── monitor/              指标采集（原子计数器、延迟直方图、Apdex）
├── network/              TCP/UDP 连接、心跳（基于 gnet）
├── protox/               动态 protobuf 加载与反射
├── robot/                机器人实例、Manager、ActionHandler
├── script/               Lua 运行时池（7 模块 68 函数）
├── state/                线程安全的键值状态存储 + 条件解析器
└── conf/
    ├── config.json       运行配置（机器人数、并发、网络、Agent）
    ├── admin-config.json Admin 服务器配置
    ├── flow/
    │   └── flow.json     流程图与动作（声明式）
    ├── adapter/
    │   ├── <proto>_<service>_codec.json  声明式 codec 配置（每连接一份）
    │   └── errors.json                   共享错误码描述（可选）
    ├── proto/            .proto 文件（动态加载）
    └── scripts/          Lua 脚本（复杂行为）
```

---

## 快速开始

```bash
# 单机模式
go run ./cmd/agent -config conf/config.json

# Agent 模式（config.json 中设置 agent.enabled: true）
go run ./cmd/agent -config conf/config.json

# Admin 服务器
go run ./cmd/admin -config conf/admin-config.json

# 前端开发
cd cmd/web && npm install && npm run dev   # http://localhost:5173
```

---

## 架构概览

### 分层依赖

```
cmd/agent → robot → engine → (ActionHandler 接口)
                 → network (gnet)
                 → script  (Lua 运行时池)
                 → protox  (动态 protobuf)
                 → state   (键值存储)
                 → adapter (协议编解码)
```

### 单次动作数据流

```
Executor 遍历节点图 → 命中 action 节点
  → ActionHandler.ExecuteAction(actionDef)
  → ActionExecutor 构建 protobuf 消息（从 state/随机源解析 bindings）
  → 序列化 → adapter 编码消息头 → gnet 发送
  → 接收响应 → adapter 解码 → 解析 S2C proto → store 字段到 state
```

### 分布式架构总览

```
浏览器 ←→ Admin (:8080) ←→ 多 Agent (:7070) → 目标游戏服务器
                │                  │
                ├── MySQL 归档     ├── Manager + N Robot
                ├── 前端静态托管   ├── 指标上报
                └── 任务调度       └── 系统监控
```

---

# 第一部分：流程设计与执行

## flow.json 结构

```json
{
  "defaultDelayMs": 500,
  "nodes":    { "nodeId": { ... }, ... },
  "actions":  { "ActionName": { ... }, ... },
  "callbacks": { "CallbackName": { ... }, ... }
}
```

- `defaultDelayMs`：全局节点间默认延迟（毫秒）。`0` = 引擎默认 1000ms，`< 0` = 禁用。
- `nodes`：JSON 对象格式，key 即节点 ID。

## 可视化编辑器

前端 FlowEditor 提供完整的可视化编辑能力：

- **画布**：拖拽创建节点，8 种节点类型面板，自动布局（dagre）
- **节点编辑抽屉**：action 编辑（pattern / proto / bindings / store / delay）
- **Proto 浏览器**：浏览已加载的 proto 消息类型和字段，用于动作编辑
- **校验报告**：实时检查 flow.json 引用完整性和语义错误（画布节点显示错误标记）
- **模板库**：可复用的 action / listen 模板
- **撤销重做**：Ctrl+Z / Ctrl+Shift+Z
- **自动保存**：编辑稿 localStorage 持久化

## 节点类型

| type       | 行为                                                                   |
| ---------- | -------------------------------------------------------------------- |
| `sequence` | 顺序执行 `next` 中列出的所有子节点                                       |
| `action`   | 执行 `action` 所引用的 `actions[name]`，唯一产生副作用的节点             |
| `loop`     | 循环执行单个 `body` 节点，支持次数/前置条件/后置条件                      |
| `boolean`  | 对 `condition` 求值，跳转到 `trueNext` 或 `falseNext`                   |
| `weighted` | 按 `options` 中的权重随机选择一个子节点执行                               |
| `wait`     | 暂停指定毫秒数（`waitMs` 或 `waitMin`~`waitMax` 随机范围）              |
| `break`    | 产生中断信号，跳出最近的 `loop`                                          |
| `continue` | 跳过本次迭代剩余步骤，进入 `loop` 的下一次迭代                            |

### 各类型 JSON 格式

```json
// sequence — 顺序执行
{ "type": "sequence", "next": ["nodeA", "nodeB", "nodeC"] }

// action — 执行动作
{ "type": "action", "action": "Login", "onError": { "strategy": "abort" }, "listenRefs": [...] }

// loop — 循环（loopCount ≤ 0 视为无限）
// body 为单个节点 ID；多步骤循环体用 sequence 节点包装后填入 body
{
  "type": "loop", "loopCount": -1,
  "condition": "lua:check.lua",
  "breakCondition": "lua:stop.lua",
  "body": "loopBodyNode"
}

// boolean — 条件分支
{ "type": "boolean", "condition": "lua:has_role.lua", "trueNext": "startGame", "falseNext": "createRole" }

// weighted — 加权随机
{ "type": "weighted", "options": [{"node": "battle", "weight": 40}, {"node": "lobby", "weight": 60}] }

// wait — 显式等待
{ "type": "wait", "waitMs": 2000 }
{ "type": "wait", "waitMin": 1000, "waitMax": 3000 }

// break / continue — 循环控制
{ "type": "break" }
{ "type": "continue" }
```

### 节点通用字段

| 字段               | 适用节点       | 说明                                                                     |
| ----------------- | -------------- | ----------------------------------------------------------------------- |
| `next`            | sequence       | 子节点 ID 列表：`["id1", "id2"]`                                         |
| `body`            | loop           | 循环体节点 ID（单个）；多步骤时指向 sequence 节点                          |
| `loopCount`       | loop           | 循环次数；`≤ 0` 为无限循环                                               |
| `condition`       | loop / boolean | loop：前置条件（false 时跳过本次）；boolean：分支判断条件                 |
| `breakCondition`  | loop           | 后置条件（true 时退出循环）                                              |
| `trueNext`        | boolean        | 条件为 true 时跳转的节点 ID                                              |
| `falseNext`       | boolean        | 条件为 false 时跳转的节点 ID                                             |
| `options`         | weighted       | 加权选项：`[{"node": "id", "weight": N}]`                                |
| `action`          | action         | 引用 `actions` 表中的动作名                                              |
| `onError`         | action         | 动作失败后的错误链路：`ignoreCodes` / `handler` / `retry` / `strategy`    |
| `listenRefs`      | action         | 注册持久化推送监听（数组）                                               |
| `waitMs`          | wait           | 等待时长（毫秒）                                                         |
| `waitMin`         | wait           | 随机等待下界（毫秒，与 `waitMax` 配合使用）                               |
| `waitMax`         | wait           | 随机等待上界（毫秒）                                                     |
| `delayMs`         | action 等      | 节点执行后延迟；`> 0` 使用此值，`= 0` 使用 defaultDelayMs，`< 0` 禁用    |

### action.onError

`onError` 定义 action 失败后的错误链路。

| 字段 | 说明 |
| ---- | ---- |
| `ignoreCodes` | 可接受的正整数错误码；命中后流程继续，但监控仍记录本次失败样本 |
| `handler` | 错误处理节点 ID；这是调用边，不写入普通 `next` |
| `retry.maxRetries` | 当前 action 的额外重试次数；`2` 表示原始执行 + 2 次重试 |
| `retry.retryDelayMs` | 每次重试前的协作式等待毫秒数 |
| `strategy` | 空 / `resume` = 继续；`skip` = 跳过当前层级；`abort` = 中断流程 |

## actions — 声明式动作

### pattern 一览

| pattern             | 作用                                                                     |
| ------------------- | ---------------------------------------------------------------------- |
| `tcpSend`           | TCP 发送不等待响应                                                      |
| `tcpRequest`        | TCP 请求-响应（一发一收 + 超时）                                        |
| `tcpConnect`        | 建立 TCP 连接（`service` + `address`，支持 `state:` 前缀取地址）       |
| `tcpClose`          | 关闭 TCP 连接（`service`）                                              |
| `tcpListen`         | 消费 TCP 推送（需 listenRefs 预注册，超时 `timeout` 秒，轮询间隔 `pollMs`） |
| `udpSend`           | UDP 发送 protobuf（使用 `c2sProto` + bindings）                         |
| `udpRequest`        | UDP 请求-响应（一发一收 + 超时）                                        |
| `udpConnect`        | 建立 UDP 连接（`service` + `address`）                                  |
| `udpClose`          | 关闭 UDP 连接（`service`）                                              |
| `udpListen`         | 消费 UDP 推送（需 listenRefs 预注册，超时 `timeout` 秒，轮询间隔 `pollMs`） |
| `httpRequest`       | HTTP 请求（`url` + `method` + `contentType` + body）                   |
| `setState`          | 从 bindings 写入 state                                                  |
| `clearState`        | 清除 state（`keys` 列表）                                                |
| `lua`               | 执行 `script` 指定的 Lua 脚本（`execute(r)` 返回 nil 表示成功，err table 表示失败） |

### ActionDef 全字段

| 字段            | 说明                                                                     |
| -------------- | ----------------------------------------------------------------------- |
| `pattern`      | 动作模式（上表 14 种）                                                   |
| `service`      | 目标服务名（`logic` / `battle` / `udp` / …）                             |
| `route`        | 不透明路由，原样传给 adapter 编码（如 `{"cmd": 3, "act": 1}`）            |
| `script`       | Lua 脚本路径（`lua` 模式）                                               |
| `address`      | 连接地址（`tcpConnect`/`udpConnect`），支持 `state:` 前缀                |
| `c2sProto`     | C2S protobuf 全名（如 `Game.TeamCreateC2S`）                              |
| `s2cProto`     | 期望的 S2C protobuf 全名                                                 |
| `bindings`     | C2S 字段绑定数组                                                         |
| `store`        | S2C 字段 → state 映射                                                    |
| `timeout`      | 超时秒数（listen / request 模式，默认 10/60）                            |
| `pollMs`       | 轮询间隔毫秒（listen 模式，默认 100）                                    |
| `keys`         | clearState 要删除的 key 列表                                              |
| `optional`     | `true`：关键字段缺失/失败时静默跳过（不中断 sequence）                   |
| `url`          | HTTP 请求 URL（`httpRequest` 模式），支持 `state:` 前缀                 |
| `method`       | HTTP 方法（`httpRequest`，默认 POST）                                    |
| `contentType`  | HTTP 内容类型（`httpRequest`，`json`（默认）/ `form`）                   |

## bindings — 字段绑定

每一项描述**一个** proto 字段的取值来源：

### 取值类

| `type`        | 取值                                                                      |
| ------------- | ------------------------------------------------------------------------ |
| `fixed`       | 固定 `value`（`type` 为空时也视为 fixed）                                 |
| `state`       | `store.Get(source)`                                                       |

### 随机类

| `type`          | 取值                                                                      |
| --------------- | ------------------------------------------------------------------------ |
| `stateFirst`    | 从 state list 取第一个元素，空列表触发跳过                                 |
| `stateRandom`   | 从 `source` state list 随机选一个（可选 `path`、`filters` 过滤）           |
| `stateRandomN`  | 从 state list 随机选 `count` 个不重复                                     |
| `stateMapKey`   | 从 state map 随机选一个 key                                               |
| `stateMapValue` | 从 state map 随机选一个 value（可选 `path`）                               |
| `randomPick`    | 从 `values` 列表随机选一个                                                |
| `randomPickN`   | 从 `values` 列表随机选 `count` 个                                          |
| `randomPickMap` | 从 `values` 按 `keySource` 查 state 得 key，再按 key 查 values 得列表，随机选一个 |
| `randomExclude` | 从 list 随机选，排除 `excludeSource` 当前值                              |
| `randomInt`     | `[min, max]` 随机整数                                                    |
| `randomBool`    | 随机布尔                                                                 |
| `randomString`  | 随机字符串（`length`，可选 `charset`；支持 `lower`/`upper`/`alpha`/`numeric`/`alphanum` 或自定义字符集） |
| `randomFloat`   | `[min, max]` 随机浮点数（可选 `precision` 小数位数）                     |

`randomString.charset` 支持别名：`lower`（a-z）、`upper`（A-Z）、`alpha`（a-zA-Z）、`numeric`（0-9）、`alphanum`（a-zA-Z0-9，默认）。其他非空字符串按自定义字符集字面量处理，例如 `"ABC-123_"`。

### 辅助类

| `type`     | 取值                       |
| ---------- | -------------------------- |
| `listSize` | state list 的长度          |

### 通用属性

- `optional: true` — 允许该字段解析失败（不跳过动作）
- `required: true` — 值缺失时跳过整个动作
- `wrap: true` — 将单个值包装为列表，用于 repeated 字段赋单个元素
- `storeAs: "key"` — 将解析结果存入 state（中间变量），后续 binding 可通过 `source` 引用
- `path: "a.b"` — 取嵌套字段，支持 `|` 分隔多条候选路径
- `condition` — 条件绑定，满足时才执行（见 ConditionDef）

### ConditionDef

```json
{ "source": "roleLevel", "op": "gt", "value": 10 }
{ "source": "status", "path": "code", "op": "eq", "value": 0 }
{ "source": "type", "op": "in", "value": [1, 2, 3], "valueSource": "state:typeList" }
```

## store — 响应字段存储

```json
"store": [
  { "field": "teamId",   "setter": "teamId" },
  { "field": "record",   "setter": "battleRecord" },
  { "setter": "loginResp" }
]
```

- `field`：S2C 响应中的字段名（支持嵌套路径如 `"record.address"`）。为空则整个响应存入 state。
- `setter`：存入 state 时使用的 key。

## callbacks — 持久化监听

```json
"callbacks": {
  "OnFrameData": {
    "s2cProto": "Game.BattleFrameDataS2C",
    "store": [{ "field": "ack", "setter": "battleAck" }]
  },
  "OnBattlePush": {
    "script": "listen_frame_data.lua"
  }
}
```

- **声明式**：`s2cProto` + `store` 自动解析并存储。
- **Lua**：`script` 指定脚本，接收 `(msgData, protoName)` 参数。

在 action 节点上通过 `listenCallbacks` 注册：

```json
{
  "type": "action",
  "action": "ConnectBattleTCP",
  "listenCallbacks": [
    { "route": {"cmd": 4, "act": 10}, "server": "battle", "callback": "OnFrameData" }
  ]
}
```

## filters — 过滤器

支持 10 种运算符：

| 运算符             | 别名        | 说明                         |
| ----------------- | ----------- | ---------------------------- |
| `eq`              | `==`        | 相等（数值感知）              |
| `neq`             | `!=`        | 不相等                        |
| `gt`              | `>`         | 大于（数值）                  |
| `gte`             | `>=`        | 大于等于                      |
| `lt`              | `<`         | 小于                          |
| `lte`             | `<=`        | 小于等于                      |
| `contains`        |             | 字符串包含                     |
| `in`              |             | 左值在右值列表中               |
| `notNil`          |             | 值非 nil                      |
| `isNil`           |             | 值为 nil                      |

```json
"filters": [
  { "path": "status",   "op": "eq", "value": 1 },
  { "path": "type",     "op": "in", "value": [1,2,3] }
]
```

## 心跳

心跳是连接级协议配置，不再作为 flow action 编排。每份 `conf/adapter/<protocol>_<service>_codec.json` 可选配置一个 `heartbeat` 对象；没有该对象表示该连接不启用心跳。默认在连接建立后按对应 codec 的心跳配置启动；若配置 `requireSecretKey: true`，则等待连接密钥设置后再启动。关闭连接时心跳自动停止。

### 静态心跳（body 固定为空）

只配置 `intervalMs` 与 `route` 时发送空 body，适用于只需要保活帧的连接。

```json
{
  "routeKeyTemplate": "{field1}:{field2}",
  "heartbeat": {
    "intervalMs": 5000,
    "route": {"field1": 2, "field2": 1}
  }
}
```

### 动态心跳（body 每次变化）

需要递增序号、状态字段、时间戳或随机值时，在 codec 的 `heartbeat.heartbeatFields` 中声明二进制布局，由连接级心跳循环按字段来源构造 body。

```json
{
  "routeKeyTemplate": "{field1}:{field2}",
  "heartbeat": {
    "intervalMs": 10000,
    "route": {"field1": 4, "field2": 2},
    "heartbeatFields": [
      {"type": "u16", "source": "counter", "start": 0, "step": 1},
      {"type": "i64", "source": "state", "key": "battleId"}
    ],
    "skipWhenMissing": true
  }
}
```

也可使用 `c2sProto + bindings` 构造 protobuf body；`c2sProto` 与 `heartbeatFields` 互斥。心跳运行时不调用业务 Lua，主流程阻塞等待时心跳仍会继续发送。

## 条件表达式

`boolean` 节点和 `loop` 的 `condition` / `breakCondition` 支持两种格式：

- **内置**：`state:key op value`，如 `state:heroId > 0`。运算符：`>= <= != == > <`。支持 `|| && !` 和括号嵌套。
- **Lua**：`lua:script_name.lua`，执行 Lua boolean 脚本，return true/false（true 满足）。

---

# 第二部分：协议适配

## Adapter 接口

Adapter 接口不变（9 方法），但实现已全 Go 化：`adapter/codec_resolver.go` 的 `CodecResolver` 按 `"<proto>:<service>"` 解析到对应 `SchemaAdapter`，后者包装 `codec/` 引擎读写声明式 schema。

| 方法                              | 说明                                                     |
| -------------------------------- | ------------------------------------------------------- |
| `HeaderSize() int`                | 消息头固定字节数（初始化时缓存，热路径零额外开销）          |
| `BodyLength(header) int`          | 从消息头解析 body 长度（纯 Go，热路径零额外开销）          |
| `EncodeTCP(route, body, key)`     | 编码 TCP 数据包（含消息头）                               |
| `EncodeUDP(route, body, key)`     | 编码 UDP 数据包（含 UDP 偏移加密）                         |
| `DecodeTCP(data, key)`            | 解码 TCP 数据包 → 路由键 + 消息体 + 错误码                 |
| `DecodeUDP(data, key)`            | 解码 UDP 数据包 → 路由键 + 消息体 + 错误码                 |
| `ExpectedRouteKey(route)`         | 从发送路由计算期望的响应路由键                              |
| `Close()`                         | 释放资源                                                   |
| `DescribeError(code)`             | 将服务端错误码映射为可读描述（读取共享 `errors.json`）        |

## 声明式 codec 配置（`<proto>_<service>_codec.json`）

每条命名连接对应一份 codec 配置，由 `CodecResolver` 按 `"<proto>:<service>"` 解析、`SchemaAdapter` 包装 `codec/` Go 引擎驱动编解码。核心字段：

- `header`：消息头字段布局，每个字段带 `name` / `size` / `role`（如 `route` / `length` / `flags` / `checksumOut` / `value` / `errorCode` / `reserved`）。
- `frame`：帧参数（`lengthIncludesHeader` / `lengthIncludesTrailer` 等），决定 `length` 字段的字节范围。
- `routeKeyTemplate`：路由键模板，如 `"{cmd}:{act}"`，占位符对应 `role:"route"` 字段。
- `pipeline`：encode/decode 管线步骤数组（每步 `op` + `algo` + `params` + `produces`）；decode 由后端自动反序执行。
- `encrypt.offset.{encode,decode}`：单向偏移加密配置（如 `udp:battle` 发送偏移 11、接收偏移 0），前 N 字节保持明文供服务端查密钥表。
- 共享 `errors.json`：`DescribeError` 读取，把服务端错误码映射为中文描述。

> 旧版 `codec.lua`（7 个必需函数）/ `error.lua` 仅保留为 T1 一致性测试的 oracle，不再参与生产编解码路径。

---

# 第三部分：Lua API 参考

加载方式：`local mod = require("<name>")`。7 个模块共 86 个函数。

## network（21 函数）

### 连接管理

| 函数                                                    | 说明                          |
| ------------------------------------------------------ | ---------------------------- |
| `connect_tcp(service, address)`                         | 建立 TCP 连接                 |
| `connect_udp(service, address)`                         | 建立 UDP 连接                 |
| `close_tcp(service)`                                    | 关闭 TCP 连接                 |
| `close_udp(service)`                                    | 关闭 UDP 连接                 |

### 请求-响应

| 函数                                                    | 说明                          |
| ------------------------------------------------------ | ---------------------------- |
| `tcp_request(service, route, msg [, s2cProto])`         | TCP 请求-响应，返回 code, data；WireBytes 自动计入监控 |
| `tcp_request_route(service, requestRoute, responseRoute, msg [, s2cProto])` | TCP 请求-响应，请求路由用于编码，响应路由用于匹配 |
| `udp_request(service, route, body [, s2cProto [, timeout]])` | UDP 请求-响应 |
| `udp_request_route(service, requestRoute, responseRoute, body [, s2cProto])` | UDP 请求-响应，请求路由用于编码，响应路由用于匹配 |

### 单向发送

| 函数                                                    | 说明                          |
| ------------------------------------------------------ | ---------------------------- |
| `tcp_send(service, route, msg)`                         | TCP 发送不等待                |
| `udp_send(service, route, body)`                        | UDP 发送                     |
| `http_request(url [, method [, contentType [, body]]])` | HTTP 请求，返回 status, body；HTTP message bytes 自动计入监控 |

### 监听轮询

| 函数                                                    | 说明                          |
| ------------------------------------------------------ | ---------------------------- |
| `tcp_listen(service, route [, s2cProto [, timeout [, pollMs]]])` | 等待 TCP 推送（需先调用 ensure_tcp_listener 预注册） |
| `udp_listen(service, route [, s2cProto [, timeout [, pollMs]]])` | 等待 UDP 推送（需先调用 ensure_udp_listener 预注册） |

### 加密密钥

| 函数                                                    | 说明                          |
| ------------------------------------------------------ | ---------------------------- |
| `set_tcp_secret_key(service, key)`                      | 设置 TCP 密钥                 |
| `get_tcp_secret_key(service)`                           | 获取 TCP 密钥                 |
| `set_udp_secret_key(service, key)`                      | 设置 UDP 密钥                 |
| `get_udp_secret_key(service)`                           | 获取 UDP 密钥                 |

### 监听占位

| 函数                                                    | 说明                          |
| ------------------------------------------------------ | ---------------------------- |
| `ensure_tcp_listener(service, routeKey)`             | 注册 TCP 监听占位             |
| `ensure_udp_listener(service, routeKey)`             | 注册 UDP 监听占位             |

### 心跳

旧的 Lua 心跳注册接口已移除；请在对应连接的 `*_codec.json` 中配置可选 `heartbeat` 对象。

## robot（14 函数）

| 函数                                       | 说明                                    |
| ----------------------------------------- | -------------------------------------- |
| `get(key)`                                | 读取 state 值                           |
| `set(key, value)`                         | 写入 state                              |
| `has(key)`                                | 检查 key 是否存在                        |
| `delete(key)`                             | 删除单个 key                            |
| `clear([key])`                            | 无参数清空全部；有参数删除指定 key         |
| `increment(key)`                          | 原子自增，返回新值                       |
| `keys()`                                  | 返回所有 key 列表                         |
| `get_path("a.b[0].c")`                   | 按路径读取嵌套值                          |
| `set_path("a.b[0].c", value)`            | 按路径写入嵌套值                          |
| `get_id()`                                | 获取机器人 ID                            |
| `get_index()`                             | 获取批次内序号（0-based）                 |
| `get_account()`                           | 获取机器人账号                            |
| `get_context()`                           | 检查 context 是否已取消                   |
| `error(code, detail)`                     | 构造 err table                           |

## proto（10 函数）

| 函数                          | 说明                                    |
| ---------------------------- | -------------------------------------- |
| `create(name)`                | 创建 protobuf 消息（返回 userdata）      |
| `set_field(msg, field, val)`  | 设置字段                                |
| `get_field(msg, field)`       | 获取字段值                              |
| `get_path(msg, path)`         | 按路径获取字段值                         |
| `get_field_map(msg)`          | 获取所有字段为 Lua table                |
| `serialize(msg)`              | 序列化为二进制                          |
| `parse(name, data)`           | 从二进制解析消息                        |
| `iter_list(msg, field)`       | 迭代 repeated 字段（for idx, item in）  |
| `list_size(msg, field)`       | repeated 字段长度                       |
| `list_get(msg, field, idx)`   | 获取 repeated 字段元素（1-based）       |

嵌套 proto 消息作为 userdata 保留；普通字段自动转为 Lua 值（int64/uint64 超 2^53 以字符串返回）。

## utils（15 函数）

| 函数                                     | 说明                                        |
| --------------------------------------- | ------------------------------------------- |
| `random_int(n)`                          | `[0, n-1]` 随机整数                          |
| `rand_range(min, max)`                   | `[min, max]` 随机整数                        |
| `random_bool()`                          | 随机布尔                                     |
| `random_string([length])`                | 随机字母数字串（默认 8）                     |
| `random_pick(table)`                     | 从数组随机选一个                              |
| `random_pick_n(table, n)`                | 从数组随机选 N 个                             |
| `weighted_pick(items, weights)`          | 加权随机，返回元素和索引                      |
| `rand_filter(items, count [, excludes])` | 排除后随机选 N 个                             |
| `rand_filter_one(items [, excludes])`    | 排除后随机选 1 个                             |
| `shuffle(arr)`                           | 原地洗牌（Fisher-Yates）                     |
| `pack_le(format, ...)`                   | 小端二进制打包（`u8/i8/u16/i16/u32/i32/u64/i64/f32/f64`） |
| `unpack_le(data, fmt1, ...)`             | 小端二进制解包，大整数超 2^53 以字符串返回    |
| `sleep(ms)`                              | 毫秒休眠（响应取消，仅暂停当前机器人主流程）   |
| `time_ms()`                              | 当前时间戳（毫秒）                           |
| `fnv_hash(str)`                          | FNV-1a 64 位哈希（返回 hex 字符串）          |

## log（4 函数）

| 函数              | 说明       |
| ---------------- | ---------- |
| `debug(msg)`      | 调试日志   |
| `info(msg)`       | 信息日志   |
| `warn(msg)`       | 警告日志   |
| `error(msg)`      | 错误日志   |

## json（2 函数）

| 函数                | 说明          |
| ------------------ | ------------- |
| `encode(table)`    | table → JSON  |
| `decode(str)`      | JSON → table  |

## Go-Lua 类型转换

- int64/uint64 超 2^53 以字符串返回（保留精度）
- Lua table → Go `map[string]any` / `[]any`
- proto 消息 → Lua userdata（支持 `__index` 方法调用）

---

# 第四部分：运行与配置

## 运行模式

### 单机模式（`agent.enabled: false`）

直接运行完整启动序列：加载配置 → Lua 适配器 → proto → 流程 → gnet → Lua 池 → Manager → 批量启动机器人。

### Agent 模式（`agent.enabled: true`）

注册到 Admin → 接收任务 → 下载配置 → 执行压测 → 上报指标。

## 任务生命周期

```
pending → starting → running → stopping → stopped
                                       → failed
```

- **创建**：前端 TaskStartModal 填写配置 → 收集资源 → multipart POST
- **启动**：Admin 分配 Agent → 下发配置 → 各 Agent 启动机器人
- **运行**：前端 RuntimeBar 实时展示状态 + 停止按钮 + 只读画布
- **停止**：优雅关闭（等待 Agent 完成 → 归档历史）
- 单例约束：同一时刻只能有一个活跃任务（starting / running / stopping）

## config.json

配置按模式拆分：`log` 和 `monitor` 两种模式共享，`standalone` 仅单机模式，`agent` 仅 Agent 模式。

### 单机模式示例

```json
{
  "log": {
    "path": "log/stressbot.log",
    "level": "info",
    "printConsole": false,
    "maxSize": 100,
    "maxBackups": 10,
    "maxAge": 30,
    "compress": false
  },
  "monitor": {
    "enabled": true,
    "reportInterval": "10s",
    "httpEnabled": false,
    "httpPort": 6060,
    "csvPath": "log/metrics.csv",
    "apdexT": 100
  },
  "standalone": {
    "bot": {
      "accountPrefix": "bot_",
      "startNumber": 1,
      "count": 100,
      "concurrentNum": 10,
      "mainService": "logic"
    },
    "stateExtra": { "key": "value" },
    "network": {
      "heartbeatInterval": "5s",
      "tcpTimeout": "60s",
      "httpTimeout": "10s"
    },
    "proto": {
      "dirs": ["conf/proto"],
      "files": []
    },
    "flow": "conf/flow/flow.json",
    "script": {
      "dirs": ["conf/scripts"]
    }
  },
  "agent": { "enabled": false }
}
```

> codec 配置：单机模式从 `<adapter 目录>/` 下的 `<proto>_<service>_codec.json`（每连接一份）+ 共享 `errors.json` 自动加载，经 `CodecResolver` 按 `"<proto>:<service>"` 解析后由 `SchemaAdapter` 驱动 `codec/` Go 引擎编解码。`codec.lua`/`error.lua` 仅保留为 T1 一致性测试 oracle，不再参与生产路径。

### Agent 模式示例

```json
{
  "log": {
    "path": "log/agent.log",
    "level": "info",
    "printConsole": true,
    "maxSize": 500,
    "maxBackups": 3,
    "maxAge": 7,
    "compress": true
  },
  "monitor": {
    "apdexT": 100,
    "reportInterval": "5s"
  },
  "agent": {
    "enabled": true,
    "adminAddr": "http://admin:8080",
    "name": "",
    "listenAddr": ":7070",
    "maxBots": 5000,
    "stressInterval": "5s",
    "systemInterval": "5s",
    "heartbeatInterval": "10s",
    "heartbeatFailInterval": "",
    "registerRetryMaxInterval": "60s",
    "maxHeartbeatFailures": 0,
    "taskRunAdminLostExit": false,
    "taskWorkDir": ""
  }
}
```

Agent 模式不需要 `standalone` 段 — 运行时参数由 Admin 通过 `TaskAssignment` 下发；codec 配置（各 `*_codec.json` + `errors.json`）随任务资源一起下发，Agent 侧经 `CodecResolver` 解析，不再读取 `adapterScript`。

### 启动策略

| 策略 | 说明 |
|------|------|
| 全量启动（默认） | 一次启动所有机器人，每 `concurrentNum` 个暂停 1 秒 |
| 渐进加压（`rampUp`） | 按 stages 分阶段启动，每阶段可覆盖 concurrency + 等待 holdSec；`reset` 阶段开始前清空已有机器人 |

含 `reset` 的渐进加压会被切分为多个**阶段段落**：历史列表中折叠为同任务下的阶段组，每段可单独查看详情/对比，时序与聚合按段号归档；无 `reset` 时仍是单条连续历史，趋势图按配置近似标注阶段切换线。详见 `docs/ramp-up.md` §6.6。

```json
"rampUp": {
  "stages": [
    { "count": 10, "concurrency": 5, "holdSec": 30 },
    { "count": 50, "concurrency": 10, "holdSec": 60 },
    { "count": 40 }
  ]
}
```

## admin-config.json

```json
{
  "listenAddr": ":8080",
  "publicUrl": "http://admin:8080",
  "staticDir": "web/dist",
  "dataDir": "data",
  "agentRegistry": {
    "unhealthyAfter": "30s",
    "offlineAfter": "60s"
  },
  "task": {
    "maxFlowSizeMB": 10,
    "deadlineDefault": "1h"
  },
  "history": {
    "enabled": true,
    "mysql": {
      "dsn": "user:pass@tcp(127.0.0.1:3306)/stressbot",
      "maxOpenConns": 10,
      "maxIdleConns": 5,
      "connMaxLifetime": "1h"
    },
    "samplerInterval": "10s",
    "retentionDays": 90,
    "pruneRunAt": "03:00"
  },
  "log": {
    "level": "info",
    "path": "log/admin.log",
    "maxSizeMB": 100,
    "maxBackups": 10
  }
}
```

---

# 第五部分：资源管理

## 资源类型

| 类型 | 目录 | 说明 |
|------|------|------|
| Proto 文件 | `conf/proto/` | 动态加载，支持全名和短名查找 |
| Lua 脚本 | `conf/scripts/` | 启动时预编译，通过 `lua:` 引用 |
| 声明式 codec 配置 | `conf/adapter/<proto>_<service>_codec.json` | 每连接一份；`CodecResolver` 按 `"<proto>:<service>"` 解析，`SchemaAdapter` 包装 `codec/` Go 引擎 |
| 错误码描述 | `conf/adapter/errors.json` | 共享；`DescribeError` 读取此文件映射服务端错误码 |

## 资源编辑器（前端 ResourcesDrawer）

- **Proto tab**：上传 / 编辑 / 删除 / 清空 + Monaco 编辑器
- **Lua tab**：同上
- **Adapter tab**：按连接编辑多份 `<proto>_<service>_codec.json` + 共享 `errors.json`；结构化视图（帧布局 / 管线 / 路由键模板编辑器）+ 源码 Monaco 切换 + 预览（调后端真实 codec 引擎跑一次 encode/decode）+ schema 校验

## 基线同步

打开编辑器或启动任务时自动对比远端（Admin 持久化）与本地（IndexedDB）资源：
- **冲突检测**：BaselineSyncModal — Monaco DiffEditor 逐个确认保留本地 / 采用远端
- **离线跳过**：用户确认保留本地的冲突记录，下次不再提示

## 任务下发流程

前端 TaskStartModal → 收集 IDB 全部资源 → multipart POST → Admin 持久化 → 下发 Agent。

---

# 第六部分：网络与状态

## 网络架构

### 连接管理

`Client` 管理多服务命名连接池：`TCPConn map[string]*Connection` + `UDPConn map[string]*Connection`。

### 请求-响应机制

`Connection.responseMap` 注册一次性 channel → 发送请求 → select 三路等待（ctx.Done / 响应 / 超时）→ defer 清理 channel。

### 持久化监听

connectionPump 解码后按 routeKey 分发：优先唤醒一次性请求响应，其次写入监听队列并执行可选 Go-store 回调；没有匹配项则丢弃。

### 帧解析

gnet `OnTraffic`：peek header → `BodyLength()` 纯 Go 计算 → read frame → adapter Decode → dispatch。

### 连接生命周期

- `onDisconnect`：意外断连触发（非主动 Close）
- `onClosed`：所有关闭都触发（监控用）
- `mainService` 断开自动停止机器人（防止僵尸连接）

## 状态管理

### Store 操作

| 方法 | 说明 |
|------|------|
| `Get / Set` | 基础读写 |
| `GetInt / GetInt32 / GetInt64 / GetString` | 类型化读取 |
| `GetList / GetMap` | 列表/映射读取 |
| `Increment / IncrementInt64` | 原子自增 |
| `Delete / Clear / Has / Keys` | 管理 |

### 路径导航

`SplitPath("a.b[0].c")` → `["a", "b", 0, "c"]`，逐层取值。

### 条件解析器

递归下降：`or → and → unary → atom → comparison`，支持 `|| && !` 和括号嵌套。

## 机器人管理

### Robot 生命周期

创建（NewRobot）→ Start（goroutine + executor.Run）→ Stop（cancel context）→ Close（释放连接 + Lua VM + state）。

### Manager 批量启动

- `StartAll`：全量 + 限速（每 concurrentNum 个暂停 1 秒）
- `StartWithRampUp`：分阶段（每阶段独立 count / concurrency / holdSec）

### 优雅关闭

`CloseAll` → 取消连接上下文并等待 connectionPump 退出 → 并行 Close + Wait。

---

# 第七部分：分布式架构

## Admin 服务器

### 核心组件

| 组件 | 说明 |
|------|------|
| TaskStore | 任务 CRUD + 状态机 + 单例约束 |
| AgentRegistry | Agent 注册 / 心跳 / 健康检查 / 离线清理 |
| MetricsAggregator | 分布式指标聚合（MergeSnapshots） |
| AgentDispatcher | 任务分配 + 配置下发 |
| HistoryStore | MySQL 历史归档（6 表） |
| Sampler | 周期采样时序数据 |

### Agent 管理

- 注册（指数退避重试）→ 心跳循环 → 健康检查（unhealthy → offline）→ 离线自动清理
- Agent 状态：`idle` / `busy` / `unhealthy` / `offline`

### 分配策略

| 策略 | 说明 |
|------|------|
| `proportional` | 按 Agent maxBots 比例分配 |
| `debug-single` | 全部分配给单个 Agent（调试用） |

### HTTP API

**Agent 上行：**

| 方法   | 路径 | 说明 |
|--------|------|------|
| POST | `/api/agent/register` | Agent 注册 |
| POST | `/api/agent/{id}/heartbeat` | Agent 心跳 |
| POST | `/api/agent/{id}/deregister` | Agent 注销 |
| POST | `/api/agent/stress` | 压测指标上报 |
| POST | `/api/agent/system` | 系统指标上报 |
| POST | `/api/agent/{id}/task/{tid}/done` | 任务完成上报 |
| GET  | `/api/agent/{id}/pending-task` | 获取待执行任务 |

**任务管理：**

| 方法   | 路径 | 说明 |
|--------|------|------|
| POST | `/api/tasks` | 创建任务（multipart） |
| GET  | `/api/tasks` | 任务列表（过滤） |
| GET  | `/api/tasks/{id}` | 任务详情 |
| GET  | `/api/tasks/{id}/config/{path}` | 下载任务配置文件 |
| POST | `/api/tasks/{id}/start` | 启动任务 |
| POST | `/api/tasks/{id}/stop` | 停止任务 |
| DELETE | `/api/tasks/{id}` | 删除任务 |

**Agent 管理：**

| 方法   | 路径 | 说明 |
|--------|------|------|
| GET | `/api/agents` | Agent 列表 |
| GET | `/api/agents/{id}` | Agent 详情 |
| DELETE | `/api/agents/{id}` | 删除离线 Agent |
| POST | `/api/agents/{id}/shutdown` | 关闭 Agent |
| POST | `/api/agents/shutdown-all` | 关闭全部 Agent |

**指标：**

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/metrics` | 聚合压测指标 |
| GET | `/api/metrics/agents` | 各 Agent 压测指标 |
| GET | `/api/system` | 聚合系统资源 |
| GET | `/api/system/agents` | 各 Agent 系统资源 |

**历史归档：**

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/history` | 历史列表（过滤/搜索/排序） |
| GET | `/api/history/tags` | 获取所有标签 |
| GET | `/api/history/{id}` | 历史详情 |
| PUT | `/api/history/{id}` | 更新元数据（starred/tags/note） |
| DELETE | `/api/history/{id}` | 删除历史 |
| GET | `/api/history/{id}/agents` | Agent 报告 |
| GET | `/api/history/{id}/config` | 原始配置 |
| GET | `/api/history/{id}/timeseries` | 时序数据 |
| POST | `/api/history/{id}/clone` | 克隆为新任务 |
| GET | `/api/history/compare` | P99 对比（2~5 条） |

**日志：**

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/logs/admin` | Admin 日志查询 |
| GET | `/api/logs/agents/{id}` | Agent 日志（代理） |
| GET | `/api/logs/admin/files` | Admin 日志文件列表 |
| GET | `/api/logs/admin/files/{name}` | 下载 Admin 日志文件 |

**资源基线：**

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/resources/baseline` | 推送资源到基线 |

**错误码：**

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/error-codes` | 查询框架错误码定义 |

## Agent 节点

### 生命周期

注册（指数退避）→ 心跳循环 → 任务轮询 → 执行 → 优雅退出。

### 任务执行（TaskRunner）

下载配置 → 加载适配器 → 编译 proto → 构建流程 → Manager → 启动机器人。

### 指标上报

- `StressReporter`：任务期间周期上报压测指标
- `SystemReporter`：始终运行，上报系统资源

### 本地 HTTP API

| 路径 | 说明 |
|------|------|
| `GET /task` | 获取当前任务 |
| `POST /stop` | 停止任务 |
| `POST /shutdown` | 关闭 Agent |
| `GET /version` | 版本信息 |
| `GET /status` | Agent 状态 |
| `GET /logs` | 日志查询 |

## 集群部署

```
Admin (:8080) ← 多 Agent (:7070) → 目标游戏服务器
```

- MySQL 历史归档：6 表 + 周期采样 + 自动裁剪
- 前端历史面板：搜索 / 标签 / 详情 / P99 对比 / 克隆重跑

---

# 第八部分：监控与日志

## 指标采集

### 原子计数器（热路径零锁）

- 成功 / 失败 / 超时 / 跳过 / 取消 / 执行中 / 字节数 / 错误分布

### 延迟直方图

16 固定桶（0~60s+）：`1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 30000, 60000` ms。

百分位：P50 / P90 / P95 / P99。

### Apdex 评分

- 当前主指标为 **RTT Apdex**，基于有完整响应帧且 `WireRTT > 0` 的网络 RTT 样本计算。
- `RTT < T` 计为满意，`T <= RTT < 4T` 计为容忍，`RTT >= 4T` 计为不满意。
- 分母为 RTT 样本数；纯客户端动作、超时、发送失败、取消且未收到响应帧的分支不进入 RTT Apdex 分母。
- T 阈值可通过 `apdexT` 配置（默认 100ms）。

## 分布式聚合

`MergeSnapshots`：跨 Agent 计数求和 + 直方图合并 + 百分位重算 + 错误合并。

### 错误码体系

动作执行错误使用单维数值 `code`（`uint64`），码段契约划分来源（无 `Kind` 维度）：

| 码段 | 来源 | 说明 |
|------|------|------|
| `< 100` | 框架码（工具自产） | 连接/编码/Lua/配置等自身故障，由 `errcode` 包 `codeRegistry` 分配 |
| `≥ 100` | 业务码（服务器返回） | 服务端 `headerErr` 原值 |

框架错误码（`errcode` 包）按层分段：

| 范围 | 层级 | 包含 |
|------|------|------|
| 1–10 | 网络层 | `ConnNotFound` / `ConnClosed` / `SendFailed` / `RecvTimeout` / `ConnDropped` / `ActionCanceled` |
| 11–20 | 协议层 | `EncodeFailed` / `ParseFailed` |
| 21–30 | 构建层 | `CreateMsg` / `BindField` / `Serialize` / `ExecFailed` |
| 31–40 | 监听层 | `ListenTimeout` / `ListenRegister` |
| 41–50 | 配置层 | `AddrEmpty` / `URLEmpty` / `URLScheme` / `UnknownPattern` / `HTTPBuild` / `HTTPReadBody` / `MarshalBody` / `HTTPStatus` / `HeartbeatConfig` |
| 51–60 | Lua 层 | `LuaNotInit` / `LuaNoScript` / `LuaExecFailed` / `LuaScriptCheck` |
| 61–70 | 回调层 | `CallbackLua` / `CallbackParse` |

监控错误分布按 `code` 单维聚合，不再按消息字符串。前端 `ErrorsTab` 按 `code < 100` 推导"框架"/"业务"标签展示，业务码可通过共享 `errors.json` 映射为可读描述。

#### errors.json 码段契约

`conf/adapter/errors.json`（扁平 `{"code":"中文描述"}`）只能包含业务码（`≥ 100`）。

- **后端撞码硬报错**：`LoadCodecResolver` 加载期发现任一 key `< 100`（占用框架保留段）立即 fail loud，错误信息含触发撞码的具体 code。
- **前端实时校验**：`ErrorMapEditor` 行式表单实时校验"码非正整数 / `< 100` 占用框架保留段 / 重复码 / 描述空"，并在顶部展示框架保留码（`< 100`，标"不可用"），存在错误时阻断保存——与后端硬报错形成双重防线。

## 前端监控面板

- **实时指标**（MonitorDock）：动作表 — 成功率 / 延迟 / Apdex / QPS / 错误分布
- **系统资源**（SystemTab）：CPU / MEM 仪表盘 + goroutine + 网络
- **趋势图**（TrendsTab）：时序变化

## 导出

| 方式 | 说明 |
|------|------|
| Console Reporter | 周期输出格式化表格 |
| HTTP JSON | `/api/metrics` |
| CSV | `csvPath` 配置 |
| pprof | `/debug/pprof/` |

## 日志系统

### 结构化日志

zap + lumberjack 轮转（`maxSize` / `maxBackups` / `maxAge` / `compress`）。

### 实时日志查看

`logview` 环形缓冲区：O(1) 写入 + cursor 分页查询 API。

### 前端日志面板

Monaco 日志查看器 + 轮询 + 文件下载。

### 告警

企业微信 webhook — DPanic 级别自动推送。

---

# 第九部分：Web 前端

## 技术栈

| 库 | 版本 | 用途 |
|---|---|---|
| React | ^18.3 | UI 框架 |
| Vite | ^5.4 | 构建工具 |
| Ant Design | ^5.21 | UI 组件库 |
| @xyflow/react | ^12.3 | 流程图编辑器 |
| Monaco Editor | ^0.52 | 代码/Proto/Lua 编辑器 |
| Zustand | ^5.0 | 状态管理 |
| zundo | ^2.2 | 撤销重做 |
| ECharts | ^6.0 | 图表 |
| protobufjs | ^7.4 | Proto 解析 |
| idb-keyval | ^6.2 | IndexedDB 持久化 |
| dagre | ^0.8 | 自动布局 |
| dayjs | ^1.11 | 时间处理 |

## 开发与构建

```bash
cd cmd/web
npm install
npm run dev    # http://localhost:5173，conf/ 挂载 + /api 代理到 Admin
npm run build  # → dist/，Admin 静态托管
npm run test   # Vitest
```

## 主题与配置

- 暗色 / 亮色切换
- antd 中文 locale
- Vite `confMountPlugin` 挂载 conf 目录

---

## 调试与 Tips

- 日志级别与输出路径由 `config.json` 的 `log` 段配置。
- Lua 脚本异常会被 `pcall` 捕获，不会导致整个机器人崩溃。
- 任何 `tcpRequest` / `tcpListen` 都可用 `optional: true` 避免非致命错误终止业务循环。
- `mainService` 配置的连接断开时自动停止该机器人（防止僵尸连接）。
- UDP 支持多服务（如 `"udp"`），通过 `udpServices` 配置。
- Admin 重启后，活跃状态的任务自动重置为 `failed`。
- Agent 心跳连续失败 `maxHeartbeatFailures` 次后自动退出（0 = 不退出）。
