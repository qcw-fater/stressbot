# stressbot — 通用游戏服务器压测工具

`stressbot` 是 `Robot` 旧压测工具的可配置化重构版本，解耦业务逻辑与框架核心：
所有消息发送/接收、字段填充、随机化、心跳、回调、条件跳转都通过 **JSON 流程配置 + 声明式动作** 表达，
少量难以通用的行为通过 **Lua 脚本** 实现。

一套 `conf/flow/flow.json + conf/scripts/*.lua` 即可驱动任意带类似协议头的游戏服务器压测。

---

## 目录结构

```
stressbot/
├── cmd/agent/            主程序入口
├── cmd/validate/         flow.json 校验器（go run ./cmd/validate conf/flow/flow.json）
├── adapter/              协议适配器接口 + Lua 桥接（消息编解码、帧分割）
├── engine/               流程执行引擎（节点图遍历、动作模式、字段绑定）
├── network/              TCP/UDP 连接、心跳（基于 gnet）
├── robot/                机器人实例、Manager
├── protox/               动态 protobuf 加载与反射
├── script/               Lua 运行时池、network/robot/utils/proto/json/log 模块
├── state/                线程安全的键值状态存储
└── conf/
    ├── config.json       运行配置（机器人数、并发、Auth、网络）
    ├── flow/
    │   └── flow.json     流程图与动作（声明式）
    ├── adapter/
    │   └── codec.lua     协议适配器脚本（消息头编解码）
    ├── proto/            .proto 文件（动态加载）
    └── scripts/          Lua 脚本（复杂行为）
```

---

## 快速开始

```bash
# 1. 准备 conf/config.json（见示例）
# 2. 验证流程配置
go run ./cmd/validate conf/flow/flow.json

# 3. 启动压测
go run ./cmd/agent -config conf/config.json
```

---

## flow.json 结构

```json
{
  "defaultDelayMs": 500,
  "nodes":    { "nodeId": { ... }, ... },
  "actions":  { "ActionName": { ... } },
  "callbacks": { "CallbackName": { ... } }
}
```

- `defaultDelayMs`：全局节点间默认延迟（毫秒）。`0` = 引擎默认 1000ms，`< 0` = 禁用。
- `nodes`：JSON 对象格式，key 即节点 ID，节点内部无需 `id` 字段。

### 节点类型

| type       | 行为                                                                   |
| ---------- | -------------------------------------------------------------------- |
| `sequence` | 顺序执行 `next` 中列出的所有子节点                                       |
| `action`   | 执行 `action` 所引用的 `actions[name]`，唯一产生副作用的节点             |
| `loop`     | 循环执行单个 `body` 节点，支持次数/前置条件/后置条件                      |
| `boolean`  | 对 `condition` 求值，跳转到 `trueNext` 或 `falseNext`                   |
| `weighted` | 按 `options` 中的权重随机选择一个子节点执行                               |
| `wait`     | 暂停 `waitMs` 毫秒                                                     |
| `break`    | 产生中断信号，跳出最近的 `loop`                                          |
| `continue` | 跳过本次迭代剩余步骤，进入 `loop` 的下一次迭代                            |

### 各类型 JSON 格式

```json
// sequence — 顺序执行
{ "type": "sequence", "next": ["nodeA", "nodeB", "nodeC"] }

// action — 执行动作
{ "type": "action", "action": "Login", "breakOff": true, "listenCallbacks": [...] }

// loop — 循环（loopCount ≤ 0 视为无限）
// body 为单个节点 ID；多步骤循环体用 sequence 节点包装后填入 body
{
  "type": "loop", "loopCount": -1,
  "condition": "lua:check.lua",        // 前置条件（可选）
  "breakCondition": "lua:stop.lua",    // 后置条件（可选）
  "body": "loopBodyNode"
}

// boolean — 条件分支
{ "type": "boolean", "condition": "lua:has_role.lua", "trueNext": "startGame", "falseNext": "createRole" }

// weighted — 加权随机
{ "type": "weighted", "options": [{"node": "battle", "weight": 40}, {"node": "lobby", "weight": 60}] }

// wait — 显式等待
{ "type": "wait", "waitMs": 2000 }

// break / continue — 循环控制
{ "type": "break" }
{ "type": "continue" }
```

### break / continue 示例

等价 Go：
```go
for i := 0; i < 10; i++ {
    StartMatch()
    if matchSucceeded { break }
}
```

flow.json：
```json
"matchRetryLoop": {
  "type": "loop", "loopCount": 10, "body": "matchRetryBody"
},
"matchRetryBody": {
  "type": "sequence",
  "next": ["StartMatch", "checkMatchResult", "retryWait"]
},
"checkMatchResult": {
  "type": "boolean", "condition": "lua:match_succeeded.lua", "trueNext": "doBreak"
},
"doBreak": { "type": "break" },
"retryWait": { "type": "wait", "waitMs": 1000 }
```

### 节点通用字段

| 字段               | 适用节点       | 说明                                                                     |
| ----------------- | -------------- | ----------------------------------------------------------------------- |
| `next`            | sequence       | 子节点 ID 列表：`["id1", "id2"]`                                         |
| `body`            | loop           | 循环体节点 ID（单个）；多步骤时指向 sequence 节点                          |
| `loopCount`       | loop           | 循环次数；`≤ 0` 为无限循环                                               |
| `condition`       | loop / boolean | loop：前置条件（false 时退出）；boolean：分支判断条件                     |
| `breakCondition`  | loop           | 后置条件（true 时退出循环）                                              |
| `trueNext`        | boolean        | 条件为 true 时跳转的节点 ID                                              |
| `falseNext`       | boolean        | 条件为 false 时跳转的节点 ID                                             |
| `options`         | weighted       | 加权选项：`[{"node": "id", "weight": N}]`                                |
| `action`          | action         | 引用 `actions` 表中的动作名                                              |
| `breakOff`        | action         | `true` = 动作失败时中断整个流程                                          |
| `listenCallbacks` | action         | 注册持久化推送监听（数组）                                               |
| `waitMs`          | wait           | 等待时长（毫秒）                                                         |
| `delayMs`         | action/boolean | 节点执行后延迟；`> 0` 使用此值，`= 0` 使用 defaultDelayMs，`< 0` 禁用    |

---

## actions[name] — 声明式动作

通用字段：

| 字段            | 说明                                                                     |
| -------------- | ----------------------------------------------------------------------- |
| `pattern`      | 动作模式（见下）                                                          |
| `service`      | 目标服务名（`logic` / `battle` / `udp` / …）                             |
| `route`        | 不透明路由，原样传给 adapter 编码（如 `{"cmd": 3, "act": 1}`）            |
| `c2sProto`     | C2S protobuf 全名（如 `Game.TeamCreateC2S`）                              |
| `s2cProto`     | 期望的 S2C protobuf 全名                                                 |
| `bindings`     | C2S 字段绑定数组（见下）                                                  |
| `store`        | S2C 字段 → state 映射                                                    |
| `optional`     | 若为 true：关键字段缺失/失败时静默跳过（不中断 sequence）                  |

### pattern 一览

| pattern             | 作用                                                                     |
| ------------------- | ---------------------------------------------------------------------- |
| `tcpSend`           | TCP 发送不等待响应                                                      |
| `tcpRequest`        | TCP 请求-响应                                                           |
| `connect`           | 建立 TCP 连接（`service` + `address` 或 state 地址）                    |
| `connectUDP`        | 建立 UDP 连接（`service` + `address`）                                  |
| `exchangeKey`       | TCP 密钥交换（`service` 指定连接；可配 `secretArg` 写 state）           |
| `close`             | 关闭连接（`target: "tcp"/"udp"` + `service`）                           |
| `clearState`        | 清除 state（`keys` 列表）                                                |
| `setState`          | 从 bindings 写入 state                                                  |
| `udpSendProto`      | UDP 发送 protobuf（使用 `c2sProto` + bindings）                         |
| `waitListen`        | 等待持久化监听到指定响应（超时 `timeout` 秒）                            |
| `registerHeartbeat` | 为连接注册心跳（`target` + `service` + `intervalMs`）                   |
| `lua`               | 执行 `script` 指定的 Lua 脚本（`execute(r)` 返回 0 表示成功）          |

---

## bindings — C2S 字段绑定

每一项描述**一个** proto 字段的取值来源：

| `type`          | 取值                                                                      |
| --------------- | ------------------------------------------------------------------------ |
| `fixed`         | 固定 `value`                                                              |
| `state`         | `store.Get(source)`                                                       |
| `stateRef`      | 同 `state`，但保留复杂结构（嵌套消息）                                     |
| `stateFirst`    | 从 state list 取第一个元素，空列表触发跳过                                 |
| `stateRandom`   | 从 `source` state list 随机选一个（可选 `path`、`filters` 过滤）           |
| `stateRandomN`  | 从 state list 随机选 `count` 个不重复                                     |
| `stateMapKey`   | 从 state map 随机选一个 key                                               |
| `stateMapValue` | 从 state map 随机选一个 value（可选 `path`）                               |
| `randomPick`    | 从 `values` 列表随机选一个                                                |
| `randomPickN`   | 从 `values` 列表随机选 `count` 个                                          |
| `randomInt`     | `[min, max]` 随机整数                                                    |
| `randomBool`    | 随机布尔                                                                 |
| `randomString`  | 随机字符串（`length`，可选 `charset`）                                    |
| `randomExclude` | 从 list 随机选，排除 `excludeSource` 当前值                              |
| `listSize`      | state list 的长度                                                        |
| `nested`        | 创建子消息并用 `bindings` 填充（`message` 指定 proto 全名）                |

通用属性：

- `optional: true` — 允许该字段解析失败（不跳过动作）
- `required: true` — 值缺失时跳过整个动作
- `wrap: true` — 将单个值包装为列表，用于 repeated 字段赋单个元素
- `storeAs: "key"` — 将解析结果存入 state（中间变量），后续 binding 可通过 `source` 引用
- `path: "a.b"` — 取嵌套字段，支持 `|` 分隔多条候选路径

---

## store — S2C 字段存储

```json
"store": [
  { "field": "teamId",            "setter": "teamId" },
  { "field": "record",            "path": "Address",    "setter": "battleAddress" },
  { "setter": "loginResp" }
]
```

`field` 为空则整个响应都存入 state。

---

## filters — 过滤器（用于 stateRandom 等）

支持 `eq/neq/gt/gte/lt/lte/in/timeWindow`：

```json
"filters": [
  { "field": "status",    "op": "eq", "value": 1 },
  { "field": "type",      "op": "in", "value": [1,2,3] },
  { "field": "startTime", "op": "timeWindow", "startTime": 0, "endTime": 99999999 }
]
```

---

## callbacks[name] — 持久化监听回调

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

- 声明式：`s2cProto` + `store` 自动解析并存储。
- Lua：`script` 指定脚本，接收 `(msgData, protoName)` 参数。

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

---

## 心跳（per-connection）

每个 TCP/UDP 连接可独立注册心跳。

### 声明式

```json
"LogicHeartbeat": {
  "pattern": "registerHeartbeat",
  "target": "tcp",
  "service": "logic",
  "route": {"cmd": 2, "act": 1},
  "intervalMs": 5000
}
```

支持 `rawBody`（二进制字段描述）或 `c2sProto` + `bindings` 构造消息体。

### Lua

```lua
local network = require("network")
local robot   = require("robot")
local utils   = require("utils")

-- Logic TCP 心跳
network.register_heartbeat("logic", 2, 1, 5000, function() return "" end)

-- Battle UDP 心跳
network.register_heartbeat("udp", 4, 2, 150, function()
    local idx = robot.increment("packageIndex") % 65536
    return utils.pack_le("u16", idx)
        .. utils.pack_le("i64", robot.get("battleId") or 0)
        .. utils.pack_le("u8",  robot.get("fighterIndex") or 0)
        .. utils.pack_le("i64", robot.get("battleSession") or 0)
end)
```

关闭连接时心跳自动停止；重复 `register_heartbeat` 会替换旧心跳。

---

## 条件表达式

`boolean` 节点和 `loop` 的 `condition` / `breakCondition` 支持两种格式：

- **内置**：`state:key op value`，如 `state:heroId > 0`、`state:roleName == ""`。运算符：`>= <= != == > <`。
- **Lua**：`lua:script_name.lua`，执行 Lua 脚本，返回 0 = true，非 0 = false。

---

## config.json 配置

| 字段                       | 说明                                              |
| ------------------------- | ------------------------------------------------- |
| `bot.accountPrefix`       | 机器人账号前缀                                     |
| `bot.startNumber`         | 起始编号                                          |
| `bot.count`               | 机器人总数                                         |
| `bot.concurrentNum`       | 并发启动数（0 = 全部同时）                          |
| `auth.address`            | Auth 服务地址                                      |
| `auth.extra`              | 额外参数（version/channel/platform）               |
| `network.tcpTimeout`      | TCP 超时（如 `"60s"`）                             |
| `network.heartbeatInterval` | 全局心跳间隔（如 `"5s"`）                         |
| `network.udpServices`     | UDP 服务名列表（如 `["udp"]`）                     |
| `network.mainService`     | 主连接服务名（断开时停止机器人，如 `"logic"`）      |
| `network.adapterPoolSize` | 适配器 Lua 池大小（默认 CPU 核数）                  |
| `proto.dirs` / `proto.files` | .proto 文件目录和路径                           |
| `adapterScript`           | 协议适配器脚本路径（默认 `conf/adapter/codec.lua`） |
| `flow`                    | 流程配置路径（默认 `conf/flow/flow.json`）          |
| `script.dirs`             | Lua 脚本目录                                       |

---

## 协议适配器

协议适配器（`adapter`）通过 Lua 脚本实现，接口包括：

| 方法                    | 说明                                                     |
| ---------------------- | ------------------------------------------------------- |
| `HeaderSize()`          | 消息头固定字节数（初始化时缓存，运行时零 Lua 调用）        |
| `BodyLength(header)`    | 从消息头解析 body 长度（纯 Go 实现，热路径零 Lua 调用）    |
| `EncodeTCP(route, body, key)` | 编码 TCP 数据包（含消息头）                            |
| `EncodeUDP(route, body, key)` | 编码 UDP 数据包（含 UDP 偏移加密）                  |
| `Decode(data, key)`     | 解码数据包 → 路由键 + 消息体 + 错误码                     |
| `ExpectedResponseKey(route)` | 从发送路由计算期望的响应路由键                         |

适配器脚本路径由 `config.json` 的 `adapterScript` 指定，可替换以适配不同协议格式的服务器。

---

## Lua API 摘要

加载方式：`local mod = require("<name>")`。

### network

| 函数                                                                | 说明                          |
| ------------------------------------------------------------------- | ---------------------------- |
| `connect_tcp(service, address)`                                     | 建立 TCP 连接                 |
| `connect_udp(service, address)`                                     | 建立 UDP 连接                 |
| `close_tcp(service)` / `close_udp(service)`                         | 关闭连接                      |
| `exchange_key(service)`                                             | 密钥交换（自动写入 TCP 连接） |
| `set_secret_key(service, key)` / `get_secret_key(service)`          | 设置/获取 TCP 密钥            |
| `set_udp_secret_key(service, key)` / `get_udp_secret_key(service)`  | 设置/获取 UDP 密钥            |
| `ensure_listener(service, cmd, act)`                                | 注册一个监听占位（nil 回调）  |
| `request(service, cmd, act, msg, s2cProto)` → `(code, resp)`        | 请求-响应（同 CMD/ACT）       |
| `request_wait(service, sendCmd, sendAct, msg, respCmd, respAct, s2cProto)` | 请求-响应（不同 CMD/ACT） |
| `send(service, cmd, act, msg)`                                      | TCP 发送不等待                |
| `udp_send(service, data)`                                           | UDP 发送原始数据              |
| `udp_send_msg(service, cmd, act, body)`                             | UDP 发送（带协议头）          |
| `wait_listen(service, cmd, act, s2cProto, timeoutSec)`              | 轮询缓存的监听消息            |
| `http_post(path, form)` → `(status, body)`                          | HTTP POST                    |
| `register_heartbeat(service, intervalMs, cmd, act, builder)`| 注册心跳（service 即连接名，TCP/UDP 由 udpServices 配置决定） |

### robot（state 存储）

| 函数                                       | 说明                                    |
| ----------------------------------------- | -------------------------------------- |
| `get(key)` / `set(key, value)`             | 读写 state                              |
| `has(key)`                                 | 检查 key 是否存在                        |
| `increment(key)` / `increment(key, delta)` | 原子自增                                 |
| `clear(key?)` / `delete(key)`              | 清除 state                               |
| `keys()`                                   | 返回所有 key                              |
| `get_path("a.b[0].c")`                     | 按路径取嵌套值                            |
| `get_id()` / `get_account()`               | 获取机器人 ID / 账号                      |
| `get_context()`                            | 检查 context 是否已取消                   |

### utils

| 函数                                     | 说明                                        |
| --------------------------------------- | ------------------------------------------- |
| `random_int(n)`                          | `[0, n-1]` 随机整数                          |
| `random_bool()`                          | 随机布尔                                     |
| `random_string(len)`                     | 随机字母数字串                               |
| `random_pick(table)`                     | 从数组随机选一个                              |
| `random_pick_n(table, n)`                | 从数组随机选 N 个                             |
| `weighted_pick(items, weights)`          | 加权随机                                     |
| `rand_range(min, max)`                   | `[min, max]` 随机整数                        |
| `rand_filter_one(items, excludes)`       | 排除后随机选 1 个                             |
| `rand_filter(items, excludes, count)`    | 排除后随机选 N 个                             |
| `sleep(ms)`                              | 毫秒休眠                                     |
| `time_ms()`                              | 当前时间戳（毫秒）                           |
| `fnv_hash(str)`                          | FNV-1a 哈希                                  |
| `pack_le(format, ...)`                   | 小端二进制打包（`u8/u16/u32/u64/i8/i16/i32/i64/bytes/time_ms/random_u16`） |

### proto

| 函数                         | 说明                                    |
| --------------------------- | -------------------------------------- |
| `create(name)`               | 创建 protobuf 消息（返回 userdata）      |
| `set_field(msg, field, val)` | 设置字段                                |
| `get_field(msg, field)`      | 获取字段值                              |
| `get_field_map(msg)`         | 获取所有字段为 Lua table                |
| `serialize(msg)`             | 序列化为二进制                          |
| `parse(name, data)`          | 从二进制解析消息                        |
| `iter_list(msg, field)`      | 迭代 repeated 字段（for idx, item in）  |
| `list_size(msg, field)`      | repeated 字段长度                       |
| `list_get(msg, field, idx)`  | 获取 repeated 字段元素（1-based）       |

嵌套 proto 消息作为 userdata 保留；普通字段自动转为 Lua 值（int64/uint64 大整数超过 2^53 以字符串形式返回以保留精度）。

### log

| 函数              | 说明       |
| ---------------- | ---------- |
| `log.info(msg)`   | 信息日志   |
| `log.warn(msg)`   | 警告日志   |
| `log.error(msg)`  | 错误日志   |

### json

| 函数                    | 说明          |
| ---------------------- | ------------- |
| `json.encode(table)`   | table → JSON  |
| `json.decode(str)`     | JSON → table  |

---

## 调试与 Tips

- 日志级别与输出路径由 `conf/config.json` 配置。
- Lua 脚本异常会被 `pcall` 捕获，不会导致整个机器人崩溃。
- 任何 `tcpRequest`/`waitListen` 都可用 `optional: true` 避免非致命错误终止业务循环。
- `go run ./cmd/validate conf/flow/flow.json` 每次改完 flow.json 后务必执行。
- `mainService` 配置的连接断开时自动停止该机器人（防止僵尸连接）。
- UDP 支持多服务（如 `"udp"`），通过 `udpServices` 配置。
