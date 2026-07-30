---
name: flow-config
description: Use when 编写或修改 stressbot 配置文件、flow.json 节点/动作/listens、onError 错误链路、声明式 codec、Lua 脚本、proto 文件、binding/filter、声明式心跳或监听配置时。
---

# stressbot 流程配置编写技能（v2）

本技能覆盖 stressbot 压测工具（v2 架构）的所有配置层，帮助用户编写正确的 `conf/flow/flow.json`、`conf/config.json`、`conf/adapter/*_codec.json`、`conf/scripts/*.lua` 和 `conf/proto/*.proto`。

> **v2 关键变化**（若你见过旧版文档，请忘掉这些旧概念）：listen 脚本回调已恢复，但通过 Robot 单线程任务队列串行执行，网络 goroutine 只投递事件、不抢 Lua LState；旧 pattern `connect`/`connectUDP`/`exchangeKey`/`close`/`udpSendProto`/`waitListen` 已拆分重组为 16 种新 pattern；顶层 `callbacks` 改名 `listens`，节点 `listenCallbacks` 改名 `listenRefs`；编解码从 `codec.lua` 改为声明式 `*_codec.json`。

---

## 一、配置文件总览

| 文件 | 作用 | 修改频率 |
|------|------|---------|
| `conf/config.json` | 运行参数（单机模式：bot/stateExtra/网络/共享状态；Agent 模式见 `agent-config.json`） | 低 |
| `conf/flow/flow.json` | 流程图定义（节点 + 动作 + 监听）— **主要配置产物** | **高** |
| `conf/scripts/*.lua` | 复杂业务逻辑脚本 | 中 |
| `conf/adapter/<proto>_<service>_codec.json` | 声明式协议编解码（每连接一份）；共享 `errors.json` 错误码描述 | 低 |
| `conf/proto/*.proto` | protobuf 消息定义 | 中 |

---

## 二、conf/config.json 完整参考（单机模式）

```json
{
  "log": {
    "level": "info",              // debug/info/warn/error
    "printConsole": true
  },
  "monitor": {
    "enabled": true,
    "httpEnabled": true,
    "httpPort": 6061,
    "apdexT": 100,                // Apdex 阈值（ms）
    "timingDetail": "rtt"         // 仅纯网络往返延迟
  },
  "pprof": { "enabled": true, "port": 6060 },
  "standalone": {
    "bot": {
      "accountPrefix": "bot_",    // 最终账号 = prefix + (startNumber + i)
      "startNumber": 20,
      "count": 100,               // 机器人总数
      "concurrentNum": 20,        // 每批并发启动数，0=全部同时
      "mainService": "logic"      // 主服务名（断线检测）
    },
    "stateExtra": {               // 注入每个 robot state 的额外字段
      "authAddr": "http://127.0.0.1:20000",
      "version": "0.31.49.171222",
      "channel": "mine",
      "platform": "1000"
    }
  },
  "agent": { "enabled": false },  // 单机模式固定 false
  "shared": {                     // Redis 共享状态（多 robot 协调用，如排位组队）
    "redis": {
      "addr": "127.0.0.1:6379", "db": 0, "keyPrefix": "stressbot",
      "defaultClaimTTL": "30s", "opTimeout": "2s"
    }
  },
  "daemon": false                 // 是否守护进程模式
}
```

- **单机模式**：`agent.enabled=false`，本进程完成全部工作。可用 flag 覆盖资源路径：`-flow`/`-proto`/`-scripts`/`-adapter`（空值回退到 `<config 所在目录>` 下默认）。
- **Agent 模式**：见 `conf/agent-config.json`（仅 `log`/`monitor`/`agent`，配置由 Admin 下发）。
- 示例：切换压测场景无需挪文件 → `go run ./cmd/agent -config conf/config.json -flow conf/flow/rank.json`。

---

## 三、conf/flow/flow.json 完整参考

### 3.1 顶层结构

```json
{
  "defaultDelayMs": 1000,   // 节点间默认延迟(ms)。0=引擎默认(1000)，<0=禁用
  "nodes":   { ... },       // 流程节点图（map[nodeId]Node），入口固定为 "main"
  "actions": { ... },       // 动作定义（map[actionName]ActionDef）
  "listens": { ... }        // 监听定义（map[listenName]ListenDef）
}
```

### 3.2 节点类型详解（8 种）

#### sequence — 顺序执行子节点
```json
"main": { "type": "sequence", "next": ["authLogin", "logicLogin", "businessLoop"] }
```
按 `next` 数组顺序执行，任一子节点出错则中止。

#### action — 执行一个动作
```json
"ConnectLogicTCP": {
  "type": "action",
  "action": "ConnectLogicTCP",      // 引用 actions 中的定义名
  "onError": {                      // 可选：ignoreCodes / handler / retry / strategy(resume|skip|abort)
    "strategy": "abort",
    "retry": { "maxRetries": 1, "retryDelayMs": 500 }
  },
  "listenRefs": [                   // 可选：注册持久推送监听（通常在连接类动作上）
    {"route": {"cmd": 3, "act": 1}, "server": "tcp:logic", "listen": "matchPoll"}
  ],
  "delayMs": 500                    // 可选：>0=使用此值，0=默认，<0=禁用
}
```
**ListenRef 字段：**
- `route`：不透明路由，传给 adapter 计算响应 key（通常是 `{"cmd":N,"act":N}`）
- `server`：格式 `"tcp:服务名"` 或 `"udp:服务名"`
- `listen`：`listens` 表中的 key；`null`/空 = 静默消费（仅入缓存队列）
- `queueSize`：监听缓存队列容量，缺省 1；显式 >0 按该值；显式 <=0 校验报错

#### loop — 循环
```json
"loadLoop": {
  "type": "loop",
  "loopCount": -1,                            // <=0=无限循环
  "condition": "state:ready==1",              // 可选前置条件，false=不进/退出循环
  "breakCondition": "state:loadProgress>=100",// 可选后置条件，true=退出循环
  "body": "LoadProgress"                      // 循环体节点 ID（多步用 sequence 包装）
}
```

#### boolean — 条件分支
```json
"hasRole": {
  "type": "boolean",
  "condition": "lua:check_role.lua",   // 条件表达式（见下）
  "trueNext": "",                       // true 时跳转（空=不跳转）
  "falseNext": "createNewRole"          // false 时跳转
}
```
**condition 表达式**（由 `cond_parser.go` 解析；严格类型、零关键字、无隐式转换）：
- `"lua:脚本名.lua"` — 调用 Lua 脚本，必须 `return true/false`（其它类型直接报错）
- `"state:<表达式>"` — `state:` 前缀的声明式表达式，文法：
  - 字面量只有数字（`123`、`1.5`）和**带引号字符串**（`"member"`）；裸标识符恒为 state 路径
  - 比较 `>=` `<=` `!=` `==` `>` `<`；算术 `+` `-` `*` `/` `%`（`%` 仅整数，除法两边整型→整除）
  - 逻辑 `&&` `||` `!` 与括号
  - 示例：`"state:index % 2 == 0"`、`"state:hp + shield > 100"`、`"state:role == \"member\""`
- **类型纪律**：裸操作数（布尔上下文）必须 bool；`>`等仅数值；`==`/`!=` 仅同类型。
  **无 `true`/`false`/`nil` 关键字**——裸 bool 直接作条件；存在性用按类型的非零比较
  （`!= 0` / `!= ""`）。任何类型不匹配或 key 缺失 → warn + 条件 false。
- 复合：`"state:rankedMatchStarted && hp > 0"`
- **state 值的 Go 类型因来源而异**（写条件/算术时需知，勿假设都是 `int64`）：
  - 内置字段：`id`/`index` 经 `robot.go` 的 `state.Set` 注入为**原生 `int`**、`account` 为 `string`
  - proto 响应字段（`store` 映射）：按 proto kind 为 `int64`/`uint64`/`float64`/`string`/`bool`
  - `setState` 的 `fixed` 值（JSON 解码）：数值→`float64`，字符串→`string`
  - Lua `robot.set`：number→`float64`/`int64`，table→map/list
  - 条件 DSL 已统一识别全部数值类型（`int`/`int64`/`uint64`/`float64` 等），写 `index % 2`、`hp > 0` 等无需关心具体类型；数值比较按整数精确（防 uint64 雪花 ID 超 2^53 失真）

#### weighted — 加权随机
```json
"businessWeight": {
  "type": "weighted",
  "options": [ {"node": "normalModel", "weight": 40}, {"node": "shop", "weight": 10} ]
}
```

#### wait — 等待
```json
"waitStart": { "type": "wait", "waitMs": 3000 }   // 或 waitMin+waitMax 随机区间
```

#### break / continue — 循环控制（仅 loop 内有效）
```json
"breakOut": { "type": "break" },  "skipIteration": { "type": "continue" }
```

### 3.3 ActionDef 动作定义详解

#### pattern 一览（16 种）

| pattern | 用途 | 必需字段 |
|---------|------|---------|
| `tcpSend` | TCP 单向发送 | `service`, `route`, `c2sProto` |
| `tcpRequest` | TCP 请求-响应 | `service`, `route`, `c2sProto`, `s2cProto` |
| `tcpConnect` | 建立 TCP 连接 | `service`, `address` |
| `tcpClose` | 关闭 TCP 连接 | `service` |
| `tcpListen` | 阻塞轮询消费 listen 缓存推送 | `service`, `route`, `s2cProto`, `timeout` |
| `udpSend` | UDP 发送 proto | `service`, `route`, `c2sProto` |
| `udpRequest` | UDP 请求-响应 | `service`, `route`, `c2sProto`, `s2cProto` |
| `udpConnect` | 建立 UDP 连接 | `service`, `address` |
| `udpClose` | 关闭 UDP 连接 | `service` |
| `udpListen` | 阻塞轮询消费 UDP listen 缓存 | `service`, `route`, `s2cProto`, `timeout` |
| `tcpHeartbeat` | 声明式 TCP 心跳（proto / raw-binary / 空 body） | `service`, `route`, `intervalMs` |
| `udpHeartbeat` | 声明式 UDP 心跳（proto / raw-binary / 空 body） | `service`, `route`, `intervalMs` |
| `httpRequest` | HTTP 请求（JSON/form） | `url`, `method`, `contentType` |
| `setState` | 设置 state 值 | `bindings` |
| `clearState` | 清除 state | `keys` |
| `lua` | Lua 脚本 | `script` |

#### tcpRequest 示例（最常用）
```json
"UserLogin": {
  "pattern": "tcpRequest",
  "service": "logic",
  "route": {"cmd": 1, "act": 1},
  "c2sProto": "Example.LoginC2S",
  "s2cProto": "Example.LoginS2C",
  "bindings": [
    {"field": "playerId", "type": "state", "source": "roleId"},
    {"field": "session", "type": "state", "source": "session"}
  ],
  "store": [ {"setter": "loginResp"} ]
}
```

#### tcpConnect 示例（动态地址）
```json
"ConnectGame": {
  "pattern": "tcpConnect",
  "service": "battle",
  "address": "state:battleAddress"   // 支持 "state:key" 动态读取，或直接 "ip:port"
}
```

#### tcpListen 示例（消费缓存推送）
```json
"StartGame": {
  "pattern": "tcpListen",
  "service": "battle",
  "route": {"cmd": 4, "act": 10},
  "s2cProto": "Game.GameStartS2C",
  "timeout": 180,     // 秒
  "pollMs": 100       // 可选，轮询间隔毫秒，默认 100
}
```

#### httpRequest 示例
```json
"AuthLogin": {
  "pattern": "httpRequest",
  "url": "state:authAddr",        // 支持 "state:key" 前缀
  "method": "POST",               // POST(默认) / GET
  "contentType": "json"           // json(默认) / form
}
```

#### tcpHeartbeat 示例（声明式二进制心跳，Go-only builder）
```json
"LogicHeartbeat": {
  "pattern": "tcpHeartbeat",
  "service": "logic",
  "route": {"cmd": 1, "act": 99},
  "intervalMs": 5000,
  "heartbeatFields": [
    {"type": "u32", "source": "stateCounter", "key": "hbSeq"},
    {"type": "u64", "source": "timestamp", "unit": "ms"}
  ],
  "skipWhenMissing": true   // state 源缺失时跳过本 tick（true）而非报错
}
```
Heartbeat body 三种形态：`c2sProto+bindings`（proto 模式）、`heartbeatFields`（raw-binary 模式）、或二者都空（静态空 body）。HeartbeatField：`type`(u8/i8/u16/i16/u32/i32/u64/i64/f32/f64，小端)、`source`(fixed/state/stateCounter/counter/timestamp/randomInt)、`value`/`floatValue`/`key`/`min`/`max`/`start`/`step`/`unit` 按 source 取。心跳每 tick 在 Go 内按布局打包，不触碰业务 LState。

#### setState / clearState / lua 示例
```json
"ResetLoadState": {
  "pattern": "setState",
  "bindings": [ {"field": "loadProgress", "type": "fixed", "value": 0} ]
}
"CleanupGame": {
  "pattern": "clearState",
  "keys": ["battleId", "battleAddress"]
}
"PostLogin": { "pattern": "lua", "script": "post_login.lua" }
```

### 3.4 FieldBind 绑定类型详解（17 种）

FieldBind 描述如何填充 C2S proto 字段。

| type | 说明 | 关键字段 |
|------|------|---------|
| `fixed` | 固定值 | `value` |
| `state` | 从 state 读取 | `source` |
| `stateFirst` | state 列表首元素 | `source`, `path` |
| `stateRandom` | state 列表随机选 1 | `source`, `path`, `filters` |
| `stateRandomN` | state 列表随机选 N 个 | `source`, `count`, `path`, `filters` |
| `stateMapKey` | state map 随机 key | `source` |
| `stateMapValue` | state map 随机 value | `source`, `path`, `filters` |
| `randomPick` | 从 `values` 随机选 1 | `values` |
| `randomPickN` | 从 `values` 选 N 个 | `values`, `count` |
| `randomPickMap` | 按 `keySource` 选子列表再随机 | `values`, `keySource` |
| `randomExclude` | 从池排除后随机选 | `values`/`source`, `excludeSource` |
| `randomInt` | 随机整数 [min,max] | `min`, `max` |
| `randomFloat` | 随机浮点 | `min`, `max`, `precision` |
| `randomBool` | 随机布尔 | — |
| `randomString` | 随机字符串 | `length`, `charset`(lower/upper/alpha/numeric/alphanum/自定义) |
| `listSize` | state 列表长度 | `source` |
| `map` | 填充 proto `map<K,V>` 字段 | `entries`（每项 `{key, value: FieldBind}`） |

**通用可选属性**：`optional`/`required`（缺值行为）、`wrap`（单值包装成 repeated）、`storeAs`（解析结果存中间 state）、`condition`（满足条件才应用本 binding）。

#### path 导航语法
- 嵌套：`"heroInfo.heroId"`
- 数组：`"[0]"`
- 管道回退：`"mailUid|gid"`（依次尝试，返回首个非 nil）

#### filters 过滤器（`stateRandom`/`stateRandomN`/`stateMapValue` 前过滤列表）
```json
"filters": [
  {"path": "modeId", "op": "neq", "value": 0},
  {"path": "status", "op": "in", "value": [1,2]}
]
```
- `op`（12 种）：`eq`/`neq`/`gt`/`gte`/`lt`/`lte`/`contains`/`notContains`/`in`/`notIn`/`notNil`/`isNil`
- `mode`：多过滤器聚合，`any`(默认,任一满足)/`all`(全部满足)/`none`(都不满足)
- 支持 `source`（值来自 state）或 `value`（字面量）

#### storeAs 中间变量 + wrap
```json
{"type": "randomPick", "values": [1,2,3], "storeAs": "_pick"},
{"field": "goodsId", "type": "state", "source": "_pick", "path": "[0]"},
{"field": "ids", "type": "stateFirst", "source": "mailList", "path": "gid", "wrap": true}
```

#### map binding（填充 proto map 字段）
```json
{
  "field": "extraData",
  "type": "map",
  "entries": [
    {"key": "ver", "value": {"type": "fixed", "value": "1.0"}},
    {"key": "ts",  "value": {"type": "state", "source": "loginTs"}}
  ]
}
```

### 3.5 StoreMapping 响应存储

将 S2C 响应字段写入 state（`tcpRequest`/`tcpListen`/`udpRequest` 及声明式 listen 都用）：
```json
"store": [
  {"field": "teamId", "setter": "teamId"},          // 单字段（field 可含嵌套路径如 "teamData.id"）
  {"setter": "loginResp"}                            // 空 field = 存整个 fieldMap
]
```
- `field`：响应 proto 中的字段名（支持嵌套 `a.b[0].c`），空=存全部字段
- `setter`：写入 state 的 key

### 3.6 ListenDef 监听定义（三种形态）

```json
"listens": {
  "stateUpdate": {                              // 形态①：声明式 store
    "s2cProto": "Game.MainStateUpdateS2C",
    "store": [ {"field": "status", "setter": "playerStatus"} ]
  },
  "matchPoll": {},                              // 形态②：纯缓存（消息入 listenQueues）
  "guildNotice": {                              // 形态③：Lua 回调（Robot 队列串行执行）
    "s2cProto": "Game.GuildNoticeS2C",
    "script": "listen_guild_notice.lua"
  },
  "frameData": {}                               // 纯缓存，主流程用 try_udp_listen pop
}
```

**三种合法形态**：
1. **声明式 store**：`s2cProto` + `store` — Go 侧 `connectionPump` 解析推送后按 store 写 state（不碰 Lua）
2. **纯缓存**：无 `s2cProto` / `store` / `script` — 消息入 `listenQueues[routeKey]`，由主流程用 `tcpListen`/`udpListen` 动作或 Lua `network.try_tcp_listen`/`try_udp_listen` 主动 pop 消费
3. **Lua 回调**：`script`（可选 `s2cProto`）— 网络 goroutine 只把事件投递到 Robot 任务队列，由 Robot owner goroutine 串行调用 `onMessage(r, msg)`；不会加锁抢 Lua LState。

---

## 四、Lua 脚本编写指南

### 4.1 脚本类型

| 类型 | 入口函数 | 返回值 | 引用方式 |
|------|---------|-------|---------|
| 动作脚本 | `execute(r)` | `nil`=成功；`robot.error(code, detail)`=失败 | action `"pattern":"lua"` |
| 条件脚本 | `execute(r)` | `true`/`false`（其它类型直接报错） | condition `"lua:xxx.lua"` / loop `breakCondition`/`condition` |
| listen 回调脚本 | `onMessage(r, msg)` | 无需返回值 | listen `{ "script": "xxx.lua" }` |

> **统一返回值约定**：动作脚本成功返回 `nil`；失败返回 `robot.error(code, detail)` err table。旧式 `return 0` / `return code` 已废弃并会 fail loud。脚本内 `network.*` 的 send/recv WireBytes 由底层 `Context` 自动累计透传给监控。条件脚本必须 `return true/false`（不再兼容 v1 的 0/1 数字）。listen 回调脚本由 Robot 队列串行执行，入口为 `onMessage(r, msg)`。

### 4.2 动作脚本模板
```lua
-- 描述：xxx 功能
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    local value = robot.get("someKey")
    local msg = proto.create("Game.XxxC2S")
    proto.set_field(msg, "field1", value)
    local err, resp = network.tcp_request("logic", {cmd=1, act=1}, msg, "Game.XxxS2C")
    if err ~= nil then
        log.error("操作失败: code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end
    local fm = proto.get_field_map(resp)
    robot.set("resultKey", fm.someField)
    return nil
end
```

### 4.3 条件脚本模板
```lua
local robot = require("robot")
function execute(r)
    local val = robot.get("someKey")
    return val ~= nil and val > 0
end
```

### 4.4 主流程消费 listen 缓存推送

推送消息由 `connectionPump` 解码后入 `listenQueues[routeKey]`。如果处理逻辑需要由主流程主动决定时，在动作脚本里用 `try_tcp_listen`/`try_udp_listen` 非阻塞 pop，再 `proto.parse` 自行解析：

```lua
-- 非阻塞 pop 一条缓存推送；队列空返回 nil, nil（不是错误）
local err, raw = network.try_tcp_listen("logic", {cmd=5, act=6})
if err ~= nil then
    return err
end
if raw ~= nil and raw ~= "" then
    -- proto.parse 失败会 RaiseError 冒泡成 framework/53，务必 pcall
    local ok, msg = pcall(proto.parse, "Game.TeamNotifyInviteS2C", raw)
    if ok then
        local fm = proto.get_field_map(msg)
        robot.set("invite", fm)   -- 自行业务处理
    end
end
```
- `try_*_listen` 单次非阻塞 pop 一条；要持续消费就在循环里反复调用，直到返回 `err == nil and raw == nil`
- 队列容量默认 1，第 2 条覆盖最旧（保最新）

### 4.5 Lua API 速查

**network 模块**：
```
-- 连接管理：统一 err-table 风格，nil 表示成功
network.connect_tcp(service, address)     -- 返回 err
network.connect_udp(service, address)     -- 返回 err
network.close_tcp(service)
network.close_udp(service)

-- TCP
network.tcp_send(svc, route, msg)         -- 单向发送，返回 err
network.tcp_request(svc, route, msg [, s2cProto])               -- 返回 err, data
network.tcp_request_route(svc, reqRoute, respRoute, msg [, s2c])-- 请求路由编码/响应路由匹配，返回 err, data
network.tcp_listen(svc, route [, s2cProto [, timeout_sec [, poll_ms]]])  -- 阻塞 pop，返回 err, data

-- UDP
network.udp_send(svc, route, msg)         -- 返回 err
network.udp_request(svc, route, msg [, s2cProto])   -- 返回 err, data
network.udp_request_route(svc, reqRoute, respRoute, msg [, s2c])
network.udp_listen(svc, route [, s2cProto [, timeout_sec [, poll_ms]]])

-- 非阻塞 pop（消费 listen 缓存队列；适合主流程主动处理高频推送）
network.try_tcp_listen(svc, route)        -- 返回 err, raw_body；队列空为 nil, nil
network.try_udp_listen(svc, route)

-- HTTP
network.http_request(url, method, contentType, body)  -- 返回 err, status, body

-- 密钥
network.set_tcp_secret_key(service, key)
network.set_udp_secret_key(service, key)
network.get_tcp_secret_key(service)
network.get_udp_secret_key(service)

-- 监听预注册（确保 routeKey 队列存在）
network.ensure_tcp_listener(service, route)
network.ensure_udp_listener(service, route)
```

**proto 模块**：
```
proto.create("Game.XxxC2S")           -- 创建消息
proto.set_field(msg, "field", value)  -- 设置字段（支持嵌套路径、table）
proto.get_field(msg, "field")         -- 读取字段
proto.parse("Game.XxxS2C", data)      -- 反序列化（失败 RaiseError，需 pcall）
proto.get_field_map(msg)              -- 所有字段 → table（repeated 为 1-based 数组）
proto.iter_list(msg, "listField")     -- 迭代 repeated
proto.list_size(msg, "listField")     -- repeated 长度
proto.list_get(msg, "listField", idx) -- repeated 索引访问
```

**robot 模块**：
```
robot.get(key) / robot.set(key, value) / robot.delete(key) / robot.has(key)
robot.get_view(key)  -- 消息类 key 的只读惰性视图（userdata），大消息只读首选
robot.get_id() / robot.get_account() / robot.increment(key)  -- 原子自增返新值
robot.clear() / robot.keys() / robot.get_path("a.b[0].c")
```

**get 与 get_view 的选择**（不可混用，误用会报错指路）：
- 整份数据要拿来自由加工/修改 → `robot.get`（独立 Lua 表，整树物化，成本 ∝ 树大小）；
- 大消息只读挑着看 → `robot.get_view`（零物化，只能用 `proto.get_field/get_path/list_size/list_get/iter_list/serialize` 窄读，不支持 `view.foo` 表语法）；
- 视图只读且是借出时数据的快照；key 为标量/Lua 表/被 `set_path` 改写过时 `get_view` 报错。范例：`conf/scripts/system_shop_buy.lua`。

**utils 模块**：
```
utils.random_int(n) / utils.rand_range(lo, hi)   -- [0,n-1] / [lo,hi]
utils.random_bool() / utils.random_string(length)
utils.random_pick(t) / utils.random_pick_n(t, n) / utils.weighted_pick(items, weights)
utils.sleep(ms)          -- 释放 LState 锁的 sleep（允许心跳/连接 pump 推进）
utils.time_ms()          -- 当前毫秒
utils.fnv_hash(version)  -- FNV-1a
utils.pack_le(format, ...)  -- 小端二进制打包：u8/u16/u32/u64/i8/i16/i32/i64/f32/f64
```

**json / log / share 模块**：
```
json.decode(str) / json.encode(table)
log.debug/info/warn/error(msg)     -- 自动带机器人 ID/账号前缀
share.*  -- Redis 共享状态（多 robot 协调，如排位组队）：hash_set/hash_get/hash_get_all/queue_push/queue_pop/incr/claim/release 等
```

### 4.6 线程安全
- Lua LState 非线程安全，由 `luaMu` 互斥锁保护
- `utils.sleep(ms)`、`tcp_listen`/`udp_listen`（阻塞版）会释放互斥锁，允许心跳/连接 pump 推进
- 声明式心跳（`tcpHeartbeat`/`udpHeartbeat`）在 Go 侧独立 goroutine 打包，不持有 LState
- 不要用 Lua 全局变量跨请求共享状态，用 `robot.get/set` 或 `share`（跨 robot）

---

## 五、声明式协议编解码（conf/adapter/）

每连接一份 `<proto>_<service>_codec.json`（如 `tcp_logic_codec.json`、`udp_battle_codec.json`），`CodecResolver` 按 `"<proto>:<service>"`（如 `tcp:logic`）解析。共享 `errors.json` 提供错误码中文描述。

### codec.json 结构
```json
{
  "version": 1,
  "endianDefault": "le",
  "frame": { "headerSize": 12, "trailerSize": 0,
             "lengthIncludesHeader": false, "lengthIncludesTrailer": false },
  "header": [
    {"name": "bodyLen", "offset": 0, "size": 4, "type": "u32", "endian": "le", "role": "length"},
    {"name": "errCode", "offset": 4, "size": 2, "type": "u16", "role": "errorCode"},
    {"name": "cmd",     "offset": 6, "size": 1, "type": "u8",  "role": "route"},
    {"name": "act",     "offset": 7, "size": 1, "type": "u8",  "role": "route"},
    {"name": "index",   "offset": 8, "size": 2, "type": "u16", "role": "value",
      "source": {"kind": "const", "value": 0}},
    {"name": "flags",   "offset": 10,"size": 1, "type": "u8",  "role": "flags",
      "bits": [{"name":"encrypted","bit":0},{"name":"compressed","bit":1}]},
    {"name": "bcc",     "offset": 11,"size": 1, "type": "u8",  "role": "checksumOut", "from":"enc.bcc"}
  ],
  "routeKeyTemplate": "{cmd}:{act}",
  "pipeline": [
    {"op": "compress", "name": "gz", "algo": "gzip", "flag": "compressed",
      "when": {"minBodyLen": 2048, "onlySmaller": true}, "onError": "fail"},
    {"op": "encrypt",  "name": "enc", "algo": "xor_carry_rol", "params": {"rol": 3},
      "keyLen": 32, "flag": "encrypted",
      "offset": {"encode": 0, "decode": 0},
      "when": {"requireKey": true, "minBodyLen": 1},
      "produces": [{"name":"bcc","algo":"xor8","region":"ciphered"}], "onError": "fail"}
  ]
}
```
- `header[]`：每项 `name/offset/size/type/endian/role` + 可选 `source`/`bits`/`from`
  - `role`：`length`/`errorCode`/`route`/`value`/`flags`/`checksumOut`
- `routeKeyTemplate`：route key 模板（如 `"{cmd}:{act}"`），listen 注册/匹配/pop 三处一致依赖它
- `pipeline[]`：`compress`/`encrypt` 等处理阶段，含 `algo`/`params`/`flag`/`offset`/`when`/`produces`
- **UDP 偏移量部分加密**：`encrypt.offset.{encode,decode}` 单向配置（前 N 字节明文供服务端查密钥表，剩余加密）

### errors.json
```json
{ "0": "成功", "1": "数据库错误", "256": "区服没有找到", ... }
```
`"code": "中文描述"`，被 `DescribeError` 用于错误日志可读化。

> ⚠️ `conf/adapter/codec.lua` 和 `error.lua` 仅保留为 T1 一致性测试的 oracle，**非生产路径**，不要修改或依赖。

---

## 六、配置编写流程

### 6.1 添加新业务流程
1. 在 `conf/proto/` 确认 C2S/S2C proto 已定义
2. 在 `actions` 添加动作定义（选 pattern，填 bindings/store）
3. 在 `nodes` 添加节点，编排执行顺序
4. 接入主流程（改 `main` 或 `businessWeight` 等入口）
5. 涉及推送：在连接类 action 的 `listenRefs` 注册，在 `listens` 定义（声明式 store 或纯缓存）
6. 校验 + 运行（见 §8）

### 6.2 选择 pattern 决策树
```
需要网络？
├─ TCP
│   ├─ 建连/关连 → tcpConnect / tcpClose
│   ├─ 发送：要响应吗？ 是→tcpRequest / 否→tcpSend
│   ├─ 消费缓存推送 → tcpListen（阻塞）/ Lua try_tcp_listen（非阻塞）
│   └─ 二进制心跳 → tcpHeartbeat（声明式，无需 Lua）
├─ UDP
│   ├─ 建连/关连 → udpConnect / udpClose
│   ├─ 发送：要响应吗？ 是→udpRequest / 否→udpSend
│   ├─ 消费缓存推送 → udpListen / Lua try_udp_listen
│   └─ 二进制心跳 → udpHeartbeat
├─ HTTP → httpRequest
└─ 复杂逻辑（重试/条件构建/二进制打包/HTTP 认证登录）→ lua
无需网络 → setState / clearState（简单）/ lua（复杂）
```

### 6.3 声明式 vs Lua
**优先声明式**：简单字段赋值、state 读取/随机、标准请求-响应、简单响应存储、声明式心跳。
**必须 Lua**：重试逻辑、条件构建消息、二进制打包（帧同步）、HTTP 认证登录、遍历列表动态构建、消费 listen 缓存推送做复杂处理。

---

## 七、常见配置模式

### 7.1 业务循环（加权随机）
```json
"businessLoop": { "type": "loop", "loopCount": -1, "body": "businessWeight" },
"businessWeight": {
  "type": "weighted",
  "options": [ {"node":"normalModel","weight":40}, {"node":"shop","weight":10} ]
}
```

### 7.2 条件等待循环（轮询）
```json
"loadLoop": {
  "type": "loop", "loopCount": -1,
  "breakCondition": "state:loadProgress>=100",
  "body": "LoadProgress"
}
```

### 7.3 持久监听 + 声明式 store 更新 state
```json
"ConnectLogicTCP": {
  "type": "action", "action": "ConnectLogicTCP",
  "listenRefs": [
    {"route":{"cmd":2,"act":18}, "server":"tcp:logic", "listen":"stateUpdate"}
  ]
}
```
```json
"stateUpdate": {
  "s2cProto": "Game.MainStateUpdateS2C",
  "store": [ {"field":"status", "setter":"playerStatus"} ]
}
```

### 7.4 高频推送 → 纯缓存 + 主流程 pop
listen 定义纯缓存，Lua 动作用 `try_udp_listen` 消费最新：
```json
"ConnectFrameUDP": {
  "type": "action", "action": "ConnectFrameUDP",
  "listenRefs": [ {"route":{"cmd":4,"act":11}, "server":"udp:frame", "listen":"frameData"} ]
}
```
```json
"frameData": {}     // 纯缓存
```
```lua
-- consume_frame_data.lua 节选
local err, raw = network.try_udp_listen("frame", {cmd=4, act=11})
if err ~= nil then
    return err
end
if raw ~= nil and #raw >= 16 then
    local b1,b2,b3,b4 = string.byte(raw, 13, 16)
    robot.set("frameAck", b1 + b2*256 + b3*65536 + b4*16777216)
end
```

### 7.5 完整业务循环模板
```json
"businessLoop": {
  "type": "sequence",
  "next": ["ConnectMainTCP", "ConnectFrameUDP", "RegisterFrameListen",
           "loadLoop", "LoadOK", "StartRun",
           "syncLoop", "FinishRun", "CollectReward", "CleanupGame"]
}
```

---

## 八、验证清单

1. **编译**：`go build ./...`
2. **配置校验**：在前端 FlowEditor 打开 `flow.json`，查看校验报告（含 listenRefs 悬空/routeKey 重复检测）
3. **运行**：`go run ./cmd/agent -config conf/config.json`（2~5 分钟）
4. **日志审查**：
   - `grep -i "error\|warn\|失败" log/stressbot.log | grep -v headError` 无异常
   - 按当前 flow 的关键动作日志确认业务循环至少完成 2 轮
   - `grep "framework/32\|仍配置已废弃" log/stressbot.log` 必须为空（旧配置/旧二进制残留检测）
5. **清理**：`rm -f log/stressbot.log`

---

## 九、注意事项

- `route` 透明传给 adapter，引擎不解析语义（通常 `{"cmd":N,"act":N}`）；routeKey 由 codec.json 的 `routeKeyTemplate` 计算
- `address` 支持 `"state:key"` 动态解析（目标服务地址可从前置响应或初始 state 取得）
- `delayMs` 默认 1 秒，`-1` 禁用，`0` 用默认
- 日志和错误信息使用中文
- 声明式心跳（`tcpHeartbeat`/`udpHeartbeat`）在 Go goroutine 打包，通过线程安全 state/私有计数器/时间/随机源构造 body，不持有 LState
- `tcpListen`/`udpListen` 的 `timeout` 单位是秒，`pollMs` 单位是毫秒
- `field` 为空的 binding 不设置 proto 字段，仅用于 `storeAs` 等中间操作
- **禁止兼容性兜底**：不写自动迁移、不用 `??` fallback，新字段全链路一致；配置错误 fail-loud（如 listen script、queueSize<=0）
- 进行测试和查找问题时每一轮尽量都要使用新号，除非是有特殊需要跑老号的情况，否则每一轮测试都应该使用新号而不是使用相同账号重复跑
