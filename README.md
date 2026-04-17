# stressbot — 通用游戏服务器压测工具

`stressbot` 是 `Robot` 旧压测工具的可配置化重构版本，解耦业务逻辑与框架核心：
所有消息发送/接收、字段填充、随机化、心跳、回调、条件跳转都通过 **JSON 流程配置 + 声明式动作** 表达，
少量难以通用的行为通过 **Lua 脚本** 实现。

一套 `conf/flow.json + conf/scripts/*.lua` 即可驱动任意带类似协议头的游戏服务器压测。

---

## 目录结构

```
stressbot/
├── cmd/stressbot/        主程序入口
├── cmd/validate/         flow.json 校验器（go run ./cmd/validate conf/flow.json）
├── engine/               流程执行引擎（动作模式、字段绑定、过滤器）
├── network/              TCP/UDP 连接、心跳、消息协议（基于 gnet）
├── robot/                机器人实例、Manager
├── protox/               动态 protobuf 加载与反射
├── script/               Lua 运行时池、network/robot/utils/proto/json 模块
├── state/                线程安全的键值状态存储
└── conf/
    ├── config.json       运行配置（机器人数、并发、Auth、网络）
    ├── header.json       消息头协议定义（XOR/GZIP/加密等）
    ├── flow.json         流程与动作（声明式）
    ├── proto/            .proto 文件（动态加载）
    └── scripts/          Lua 脚本（复杂行为）
```

---

## 快速开始

```bash
# 1. 准备 conf/config.json（见示例）
# 2. 验证流程配置
go run ./cmd/validate conf/flow.json

# 3. 启动压测
go run ./cmd/stressbot -config conf/config.json
```

---

## flow.json 结构

```json
{
  "startNode": "start",
  "nodes":    [ ... 节点列表 ... ],
  "actions":  { "ActionName": { ... } },
  "callbacks": { "CallbackName": { ... } }
}
```

### 节点类型

| type       | 行为                                                                   |
| ---------- | -------------------------------------------------------------------- |
| `start`    | 起始节点，**顺序**执行 `next` 下所有子节点（与 `sequence` 等价）        |
| `sequence` | 顺序执行 `next` 下所有子节点                                          |
| `action`   | 执行 `action` 所指定的 `actions[name]`                                |
| `loop`     | 循环执行 `next` 共 `loopCount` 次（`-1` 为无限循环，被 `context` 取消）|
| `boolean`  | 执行 `condition`/`action`（Lua 或内置），按结果走 `trueNext`/`falseNext` |
| `weighted` | 按 `next[i].weight` 概率抽取**一个**子节点执行                         |
| `wait`     | 暂停 `waitSeconds`（或旧写法 `value`）                                |

节点通用字段：

| 字段               | 说明                                                                  |
| ----------------- | ------------------------------------------------------------------- |
| `id`              | 节点 ID                                                              |
| `type`            | 节点类型                                                             |
| `next`            | 子节点数组：`[{ "node": "xxx", "weight": 5 }, ...]`                   |
| `action`          | 配套 `actions[name]`；`boolean` 节点也可把条件表达式放这里（旧式兼容） |
| `breakOff`        | 执行失败时中断父 sequence                                            |
| `loopCount`       | `loop` 节点循环次数                                                  |
| `condition`       | `boolean` 节点条件表达式，`lua:file.lua` 形式走 Lua                  |
| `trueNext` / `falseNext` / `trueBranch` / `falseBranch` | 布尔分支      |
| `listenCallbacks` / `listen` | 注册持久化监听（数组）                                   |
| `delayMs`         | 节点执行后延时（毫秒）                                               |

### actions[name] — 声明式动作

通用字段：

| 字段            | 说明                                                                     |
| -------------- | ----------------------------------------------------------------------- |
| `pattern`      | 动作模式（见下）                                                          |
| `service`      | 目标 TCP 服务名（`logic` / `battle` / …）                                |
| `cmd` / `act`  | 协议头 CMD/ACT                                                           |
| `c2sProto`     | C2S protobuf 全名（如 `Game.TeamCreateC2S`）                              |
| `s2cProto`     | 期望的 S2C protobuf 全名                                                 |
| `bindings`     | C2S 字段绑定数组（见下）                                                  |
| `store`        | S2C 字段 → state 映射                                                    |
| `respCmd` / `respAct` | `tcpRequest` 等待与发送不同 CMD/ACT 的响应                         |
| `optional`     | 若为 true：关键字段缺失/失败时静默跳过（不中断 sequence）                  |
| `delay`        | 动作执行完成后 sleep 毫秒数                                              |

### pattern 一览

| pattern             | 作用                                                                     |
| ------------------- | ---------------------------------------------------------------------- |
| `tcpSend`           | TCP 发送不等待响应                                                      |
| `tcpRequest`        | TCP 请求-响应（可 `respCmd`/`respAct` 等待不同回包）                     |
| `udpSendProto`      | UDP 发送 protobuf（使用 `c2sProto` + bindings）                         |
| `udpSendRaw`        | UDP 发送自定义二进制（见 `rawBody`）                                    |
| `httpPost`          | HTTP POST 到 Auth 服务                                                  |
| `connect`           | 建立 TCP 连接（`service` + `address` 或 state 地址）                    |
| `connectUDP`        | 建立 UDP 连接                                                          |
| `exchangeKey`       | TCP 密钥交换（`service` 指定连接；可配 `secretArg` 写 state）           |
| `close`             | 关闭连接（`target: "tcp" / "udp"` + `service`）                         |
| `clearState`        | 清除 state（`keys` 列表）                                                |
| `setState`          | 从 bindings 写入 state                                                  |
| `waitListen`        | 等待持久化监听到指定 CMD/ACT 响应（超时 `timeoutSeconds`）               |
| `registerHeartbeat` | 为连接注册心跳（见 §心跳）                                              |
| `sleep`             | sleep `delay` 毫秒                                                      |
| `lua`               | 执行 `script` 指定的 Lua 脚本（`execute(r)` 返回 0 表示成功）          |

### bindings — C2S 字段绑定

每一项描述**一个** proto 字段的取值来源：

| `type`          | 取值                                                                      |
| --------------- | ------------------------------------------------------------------------ |
| `fixed`         | 固定 `value`                                                              |
| `state`         | `store.Get(source)`                                                       |
| `stateRef`      | 同 `state`，但保留复杂结构（嵌套消息）                                     |
| `stateRandom`   | 从 `source` state list 随机选一个（可选 `path` 取嵌套字段、`filters` 过滤）|
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
| 任何项           | `optional: true` 时允许该字段解析失败（不跳过动作）                        |

### store — S2C 字段存储

```json
"store": [
  { "field": "teamId",            "setter": "teamId" },
  { "field": "record",            "path": "Address",    "setter": "battleAddress" },
  { "setter": "loginResp" }        // field 为空则整个响应都存入
]
```

### filters — 过滤器（用于 stateRandom 等）

支持 `eq/neq/gt/gte/lt/lte/in/timeWindow`：

```json
"filters": [
  { "field": "status",    "op": "eq", "value": 1 },
  { "field": "type",      "op": "in", "value": [1,2,3] },
  { "field": "startTime", "op": "timeWindow", "startTime": 0, "endTime": 99999999 }
]
```

---

## 心跳（per-connection）

每个 TCP/UDP 连接可独立注册心跳，interval、CMD/ACT、payload 构建方式都可配。

### 声明式

```json
"LogicHeartbeat": {
  "pattern": "registerHeartbeat",
  "target": "tcp",
  "service": "logic",
  "cmd": 2, "act": 1,
  "intervalMs": 5000
}
```

支持 `rawBody`（二进制字段描述，参考 `udpSendRaw`）或 `c2sProto` + `bindings` 构造消息体。

### Lua

```lua
local network = require("network")
local robot   = require("robot")
local utils   = require("utils")

-- Logic TCP 心跳：5 秒，空 body
network.register_heartbeat("tcp", "logic", 2, 1, 5000, function() return "" end)

-- Battle UDP 心跳：150ms，完整二进制 body
network.register_heartbeat("udp", "battle", 4, 2, 150, function()
    local idx = robot.increment("packageIndex") % 65536
    return utils.pack_le("u16", idx)
        .. utils.pack_le("i64", robot.get("battleId") or 0)
        .. utils.pack_le("u8",  robot.get("fighterIndex") or 0)
        .. utils.pack_le("i64", robot.get("battleSession") or 0)
        -- ...
end)
```

关闭连接时心跳自动停止；重复 `register_heartbeat` 会替换旧心跳。

---

## Lua API 摘要

加载方式：`local mod = require("<name>")`。

### network

| 函数                                                                | 说明                          |
| ------------------------------------------------------------------- | ---------------------------- |
| `connect_tcp(service, address)`                                     | 建立 TCP 连接                 |
| `connect_udp(address)`                                              | 建立 UDP 连接                 |
| `close_tcp(service)` / `close_udp()`                                | 关闭连接                      |
| `exchange_key(service)`                                             | 密钥交换（自动写入 TCP 连接） |
| `set_secret_key(service, key)` / `set_udp_secret_key(key)`          | 手动设置密钥                  |
| `ensure_listener(service, cmd, act)`                                | 注册一个监听占位（nil 回调）  |
| `request(service, cmd, act, msg, s2cProto)` → `(code, resp)`        | 请求-响应（同 CMD/ACT）       |
| `request_wait(service, sendCmd, sendAct, msg, respCmd, respAct, s2cProto)` | 请求-响应（不同 CMD/ACT） |
| `send(service, cmd, act, msg)`                                      | TCP 发送不等待                |
| `udp_send(data)` / `udp_send_msg(cmd, act, body)`                   | UDP 发送（带/不带协议头）     |
| `wait_listen(service, cmd, act, s2cProto, timeoutSec)`              | 轮询缓存的监听消息            |
| `http_post(path, form)` → `(status, body)`                          | HTTP POST                    |
| `register_heartbeat(target, service, cmd, act, intervalMs, builder)`| 注册心跳                      |

### robot（state 存储）

`get` / `set` / `has` / `increment` / `clear(key?)` / `delete(key)` /
`keys()` / `get_path("a.b[0].c")` / `get_id()` / `get_account()` /
`get_context()`（context 是否已取消）。

### utils

`random_int(min,max)` / `random_bool()` / `random_string(len)` /
`random_pick(table)` / `random_pick_n(table,n)` /
`weighted_pick(items, weights)` — 带权随机 /
`rand_range(min,max)` — 对齐旧 `RandRangeNumber` /
`rand_filter_one(items, excludes)` — 对齐旧 `RandSilenceFilterOne` /
`rand_filter(items, excludes, count)` /
`sleep(ms)` / `time_ms()` / `fnv_hash(str)` /
`pack_le(format, values...)`（`u8/u16/u32/u64/i8/i16/i32/i64/bytes/time_ms/random_u16`）/
`log_info(msg)` / `log_error(msg)`。

### proto

`create(name)` → 消息 userdata；`set_field(msg, field, value)` / `get_field(msg, field)` /
`get_field_map(msg)` → 字段 table；`serialize(msg)` / `parse(name, data)` /
`iter_list(msg, field)`（for idx, item in ...）/ `list_size(msg, field)` /
`list_get(msg, field, idx)`（1-based）。

嵌套 proto 消息作为 userdata 保留；普通字段自动转为 Lua 值（int64/uint64 大整数超过 2^53 以字符串形式返回以保留精度）。

---

## 从 Robot 迁移的要点

1. **心跳**：旧工具各服务心跳在 `OnHandleConnectXXX` 里硬编码。新版全部通过
   `network.register_heartbeat` 或声明式 `registerHeartbeat` 动作注册，每条连接独立。
2. **加密/密钥**：TCP 密钥由 `exchangeKey` 自动写入；UDP 密钥需要 `set_udp_secret_key`
   （通常是从 `BattleStartLoadingS2C` 中的 `FighterList[i].secretKey` 取）。
3. **战斗收尾**：旧工具关闭连接靠底层自动回收，新版显式在 `game_over.lua` 里
   `close_udp()` / `close_tcp("battle")` 并清理 state（`battleId`/`fighterIndex` 等），
   以保证 `businessLoop` 下一轮能重新匹配/连接。
4. **IRobot → state.Store**：旧工具用 `r.GetXxx()`，新版从 state 读；字段名约定：
   `roleId` / `session` / `battleId` / `fighterIndex` / `battleSecretKey` /
   `battleSession` / `battleAck` / `packageIndex` / `heroIdList`。
5. **随机化**：
   - `RandRangeNumber(min,max)` → binding `randomInt` 或 `utils.rand_range`
   - `RandSilenceOne(list)`     → binding `stateRandom` 或 `utils.random_pick`
   - `RandSilenceFilterOne(list, excludes)` → binding `randomExclude` 或 `utils.rand_filter_one`
6. **持久化监听**：
   - 声明式：节点上加 `listenCallbacks: [{cmd, act, server, callback}]`
   - `callback` 指向 `flow.callbacks` 中的一项（声明式 store 或 Lua 脚本）
   - 监听回调自动在对应服务的 TCP 连接上 goroutine 内分发；Lua 回调通过 mutex 串行化
7. **验证**：`go run ./cmd/validate conf/flow.json` 会报告缺失节点/动作/回调。

---

## 调试与 Tips

- 日志级别与输出路径由 `conf/config.json` 或命令行参数控制。
- Lua 脚本异常会被 `pcall`/`defer` 捕获，避免整个机器人崩溃。
- 任何 `tcpRequest`/`waitListen` 都可用 `optional: true` 避免非致命错误终止业务循环。
- `go run ./cmd/validate <flow.json>` 每次改完强烈建议跑一次。
