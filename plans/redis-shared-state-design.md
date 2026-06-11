# Redis Shared State 设计方案

## 1. 背景

当前 stressbot 的状态模型分为两类：

- `state.Store`：单个 Robot 私有状态，用于保存登录响应、战斗上下文、监听回调结果等；Lua 通过 `robot.get/set` 访问。
- `flow.json` / Lua 脚本：描述单个 Robot 的行为流程；不同 Robot 之间没有内置协作通道。

这对单排、独立循环、普通请求-响应压测足够，但无法稳定表达“多个机器人围绕同一资源协作”的流程，例如：

- 排位双排 / 三排：队长发布招募，队员读取队伍信息并加入，队长等满员后开匹配。
- 队伍池：一批机器人发布可加入队伍，另一批机器人领取并加入。
- 好友池 / 公会池 / 活动报名池：多个机器人跨流程共享临时资源。
- 分布式任务分片：多个 Agent 执行同一任务时抢占不同数据片段，避免重复处理。

这类能力本质上接近 Redis 的共享状态、计数器、锁、队列和 Hash。既然该能力属于高级复杂流程能力，设计上不提供进程内 memory 版本，直接以 Redis 作为唯一共享后端，避免用户误以为本地共享也能跨 Agent 生效。

## 2. 目标

1. 提供一个 Redis-backed shared state 能力，让同一任务内的多个 Robot 可以通过 Lua 共享协作状态。
2. 支持跨进程、跨 Agent 共享；只要多个 Agent 连接同一个 Redis 且使用同一任务命名空间，即可共享数据。
3. Lua 侧以独立模块 `share` 暴露通用状态原语，不把接口挂到 `robot` 模块上。
4. Go 引擎层不内置排位、招募、组队等业务语义；业务编排由用户 Lua 脚本负责。
5. Redis key 自动带任务命名空间，避免不同任务之间互相污染。
6. 正常任务结束时按任务索引集合清理所有共享 key；TTL 作为可选业务过期和异常退出兜底。
7. 前端同步更新 Lua API 文档、脚本提示、任务配置类型、校验报告和接口文档。

## 3. 非目标

1. 不提供完整 Redis 客户端，不开放任意 Redis 命令。
2. 不提供 `team_publish`、`rank_join`、`recruit_wait` 之类业务 API。
3. 不提供进程内 memory shared store。
4. 不把 shared state 作为配置持久化或历史数据存储。
5. 不支持 Redis Pub/Sub、Stream、ZSet、Bitmap、HyperLogLog 等非第一阶段必要类型。
6. 不保证异常崩溃后所有未设置 TTL 的 key 立即消失；通过 runId 隔离保证不影响新任务，通过正常停止清理保证常规路径无残留。

## 4. 已确认设计决策

| 决策点 | 结论 |
|---|---|
| 后端 | 只支持 Redis，不做 memory 后端 |
| Lua 模块名 | `share`，通过 `local share = require("share")` 使用 |
| 是否挂到 robot | 不挂到 `robot.shared_*`，避免 robot 模块职责膨胀 |
| API 形态 | 类 Redis 但受控的 shared state 原语，不是完整 Redis 客户端 |
| 业务语义 | 不内置排位/招募/组队，全部由用户 Lua 编排 |
| key 隔离 | Go 层自动加 `keyPrefix:runId:type:userKey` 前缀 |
| 清理 | 写入时维护任务 key 索引集合；任务结束时批量删除 |
| TTL | 普通写入可选；claim 默认使用租约 TTL |
| Value 编码 | JSON 编码 Lua 值；读出后还原为 Lua 值 |
| 大整数 | 建议超大 int64 ID 在 Lua shared state 中用字符串存储 |

## 5. 总体架构

```text
Lua 脚本
  │
  │ local share = require("share")
  │ share.get / set / claim / queue_push / hash_set ...
  ▼
script/api_share.go
  │
  │ 从 script.Context 获取 SharedStore
  ▼
sharedstate.Store 接口
  │
  │ RedisStore 实现
  ▼
Redis
```

关键结构：

```text
Admin / Standalone config
  └─ SharedConfig
       └─ RedisConfig

Task startup
  └─ 创建 runId
  └─ 创建 sharedstate.RedisStore
  └─ 注入 robot.ManagerConfig
       └─ 注入 robot.Config
            └─ 注入 script.Context
                 └─ share Lua module 使用
```

### 5.1 包边界建议

新增顶层包：

```text
sharedstate/
  config.go        // SharedConfig / RedisConfig / Resolve
  store.go         // Store interface + 公共类型
  redis_store.go   // Redis 实现
  codec.go         // JSON 编解码，Lua/Go 共享值格式约束
  scripts.go       // Redis Lua 脚本常量，如 release、incr+expire 等
```

脚本 API 放在：

```text
script/api_share.go
```

并在 `script/runtime.go` 的 `registerAPIs` 中新增：

```go
L.PreloadModule("share", loadShareModule)
```

### 5.2 与现有 Context 的关系

`script.Context` 新增字段：

```go
Shared sharedstate.Store
```

`Robot` 创建 Lua Context 时把同一个任务级 `SharedStore` 注入进去。没有配置 Redis 或任务未启用共享状态时，`Shared` 为 nil，`share.*` API 返回明确错误，不 panic。

**注意：`script.Context` 在 `robot/robot.go` 里有两个构造点，都必须注入 `Shared`：**

1. `Robot.Start()` 内构造的 Context —— 供 `action` / `boolean`（条件节点、loop breakCondition）脚本使用。
2. `createListenCallback()` 回调闭包内每次重新构造的 Context —— 供持久化监听回调脚本使用。

如果只在第 1 处注入，监听回调脚本里调用 `share.*` 会永远拿到"未启用"错误，即使任务实际已启用共享状态。两处必须保持一致。

## 6. Redis key 设计

不采用“所有状态塞进一个大 Hash”的方案。原因：Redis 的类型挂在 key 上，不挂在 hash field 上；如果所有数据塞进一个大 Hash，会丢失原生 List、SET NX EX、key TTL 等能力，并需要在应用层重写 field TTL、队列 pop、claim 过期等语义。

采用“任务命名空间 + Redis 原生类型 + key 索引集合”：

```text
<prefix>:<runId>:kv:<userKey>
<prefix>:<runId>:counter:<userKey>
<prefix>:<runId>:claim:<userKey>
<prefix>:<runId>:queue:<userKey>
<prefix>:<runId>:hash:<userKey>
<prefix>:<runId>:keys
```

默认：

```text
prefix = stressbot
```

示例：

Lua：

```lua
share.set("rank:team:100", data)
share.claim("rank:team:100:slot:1", robot.get_account())
share.queue_push("rank:team_pool", data)
share.hash_set("rank:team:100", "status", "recruiting")
```

Redis 实际 key：

```text
stressbot:task_abc:kv:rank:team:100
stressbot:task_abc:claim:rank:team:100:slot:1
stressbot:task_abc:queue:rank:team_pool
stressbot:task_abc:hash:rank:team:100
```

### 6.1 key 索引集合

每次写入一个真实 Redis key 时，记录到：

```text
stressbot:<runId>:keys
```

例如：

```redis
SADD stressbot:task_abc:keys stressbot:task_abc:kv:rank:team:100
SADD stressbot:task_abc:keys stressbot:task_abc:claim:rank:team:100:slot:1
```

任务结束时：

```redis
SMEMBERS stressbot:task_abc:keys
DEL <all returned keys>
DEL stressbot:task_abc:keys
```

实现注意：

- 索引集合可能很大。`SMEMBERS` 一次性返回全部，超大任务（百万级 key）建议改用 `SSCAN` 迭代游标，边扫边删，避免一次性拉回全部成员撑爆内存 / 阻塞 Redis。
- `DEL` 要分批，避免一次命令参数过多。建议每批 500 或 1000 个 key。
- 索引集合本身是单一热 key，数千机器人并发 `SADD` 会在该 key 上形成写热点。第一版可接受；如成为瓶颈，可按 key 类型或 hash 分片成多个索引集合（如 `:keys:0` ~ `:keys:N`）。

### 6.2 runId 来源

- Admin 分布式任务：使用任务 ID 或任务运行 ID。优先使用稳定且唯一的 task run id。
- 单机模式：启动时生成 `standalone-<unixMilli>-<pid>`。

runId 不由 Lua 用户传入，防止脚本跨任务访问。

## 7. Lua API 设计

所有 API 位于 `share` 模块：

```lua
local share = require("share")
```

所有返回风格统一为双返回值：

```lua
value_or_ok, err
```

约定：

- `err == nil` 表示调用本身成功。
- `err ~= nil` 表示 Redis、序列化、参数或配置错误。
- key 不存在不是错误，返回 `nil, nil` 或 `false, nil`。
- API 不主动中断 Lua 脚本；脚本根据 err 决定是否 `return -1`。

### 7.1 KV

#### `share.set(key, value [, ttlSec]) -> ok, err`

写入共享值。

```lua
local ok, err = share.set("rank:team:100", {
    teamId = "123456789012345678",
    tsId = 2003,
    model = 2,
})
```

- `ttlSec` 可选。
- 不传 TTL：key 存活到任务结束清理或手动删除。
- 传 TTL：Redis key 自动过期。

Redis 映射：

```redis
SET <kvKey> <json>
SET <kvKey> <json> EX <ttlSec>
SADD <indexKey> <kvKey>
```

#### `share.get(key) -> value, err`

读取共享值。

- key 不存在：`nil, nil`
- 成功：`value, nil`
- 错误：`nil, err`

Redis 映射：

```redis
GET <kvKey>
```

#### `share.del(key) -> ok, err`

删除 KV key。

Redis 映射：

```redis
DEL <kvKey>
```

是否从索引集合移除该 key 可选；即使不移除，任务结束时再次 DEL 不影响正确性。为了减少 Redis 写放大，第一版可以不 `SREM`。

#### `share.exists(key) -> exists, err`

判断 KV key 是否存在。

Redis 映射：

```redis
EXISTS <kvKey>
```

#### `share.expire(key, ttlSec) -> ok, err`

刷新 KV key 的 TTL。

- `ttlSec > 0`。
- key 不存在：`false, nil`。

Redis 映射：

```redis
EXPIRE <kvKey> <ttlSec>
```

### 7.2 Counter

#### `share.incr(key [, delta [, ttlSec]]) -> value, err`

原子递增或递减计数。

```lua
local n, err = share.incr("rank:team:100:joined")
local n, err = share.incr("rank:team:100:joined", 1, 60)
```

- `delta` 默认 `1`。
- `ttlSec` 可选。
- 不传 TTL：存活到任务结束清理。
- 传 TTL：每次 incr 刷新 TTL。

Redis 映射：

```redis
INCRBY <counterKey> <delta>
EXPIRE <counterKey> <ttlSec>       -- 仅 ttlSec 存在时
SADD <indexKey> <counterKey>
```

为了保证 `INCRBY + EXPIRE + SADD` 行为一致，RedisStore 内部使用 Redis Lua 脚本封装。

### 7.3 Claim / Lock

Claim 是共享资源抢占原语，适合队伍 slot、任务分片、一次性资源领取。Claim 与普通 KV 不同，默认有短租约，避免机器人异常退出导致任务期间死锁。

#### `share.claim(key, owner [, ttlSec]) -> ok, err`

尝试抢占资源。

```lua
local ok, err = share.claim("rank:team:100:slot:1", robot.get_account())
local ok, err = share.claim("rank:team:100:slot:1", robot.get_account(), 120)
```

- 只有 key 不存在时成功。
- `owner` 通常使用 `robot.get_account()` 或 `tostring(robot.get_id())`。
- `ttlSec` 可选；不传时使用 `shared.redis.defaultClaimTTL`，默认 30 秒。
- 成功：`true, nil`
- 已被占用：`false, nil`
- 错误：`false, err`

Redis 映射：

```redis
SET <claimKey> <owner> NX EX <ttlSec>
SADD <indexKey> <claimKey>
```

#### `share.release(key, owner) -> released, err`

释放自己持有的资源。

- 当前 owner 匹配时删除并返回 `true, nil`。
- key 不存在或 owner 不匹配返回 `false, nil`。

Redis 映射使用 Lua 脚本，避免误删别人后来抢到的 claim：

```lua
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0
```

#### `share.owner(key) -> owner, err`

读取当前 claim owner。

- 无 owner：`nil, nil`
- 有 owner：`string, nil`

Redis 映射：

```redis
GET <claimKey>
```

#### `share.renew(key, owner [, ttlSec]) -> ok, err`

刷新自己持有的 claim 租约。

- owner 匹配时刷新 TTL 并返回 `true, nil`。
- owner 不匹配或 key 不存在返回 `false, nil`。
- 不传 ttlSec 使用默认 claim TTL。

Redis 映射使用 Lua 脚本：

```lua
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("EXPIRE", KEYS[1], ARGV[2])
end
return 0
```

### 7.4 Queue / List

Queue 使用 Redis List，方向固定：尾部 push，头部 pop，即 FIFO。

#### `share.queue_push(key, value [, ttlSec]) -> ok, err`

向队列尾部追加元素。

```lua
local ok, err = share.queue_push("rank:team_pool", teamInfo)
```

- `ttlSec` 是整个队列 key 的 TTL，不是单个元素 TTL。
- 元素级过期由用户在 value 中存 `expireAt` 并在 pop 后自行判断。

Redis 映射：

```redis
RPUSH <queueKey> <json>
EXPIRE <queueKey> <ttlSec>          -- 仅 ttlSec 存在时
SADD <indexKey> <queueKey>
```

#### `share.queue_pop(key) -> value, err`

从队列头部弹出元素。

- 队列不存在或为空：`nil, nil`
- 成功：`value, nil`

Redis 映射：

```redis
LPOP <queueKey>
```

#### `share.queue_len(key) -> n, err`

返回队列长度。

Redis 映射：

```redis
LLEN <queueKey>
```

#### `share.queue_expire(key, ttlSec) -> ok, err`

刷新整个队列 key 的 TTL。

Redis 映射：

```redis
EXPIRE <queueKey> <ttlSec>
```

### 7.5 Hash

Hash 用于结构化共享对象。适合队伍状态、房间状态、资源状态等多个字段独立更新的场景。

#### `share.hash_set(key, field, value [, ttlSec]) -> ok, err`

写入 hash 字段。

```lua
share.hash_set("rank:team:100", "status", "recruiting")
share.hash_set("rank:team:100", "teamId", "123456789012345678")
```

- TTL 是整个 hash key 的 TTL，不是 field TTL。
- value 使用 JSON 编码。

Redis 映射：

```redis
HSET <hashKey> <field> <json>
EXPIRE <hashKey> <ttlSec>           -- 仅 ttlSec 存在时
SADD <indexKey> <hashKey>
```

#### `share.hash_get(key, field) -> value, err`

读取单个字段。

Redis 映射：

```redis
HGET <hashKey> <field>
```

#### `share.hash_get_all(key) -> table, err`

读取整个 hash。

- hash 不存在：`nil, nil` 或 `{}` 的语义需要固定。建议返回 `nil, nil`，与 `share.get` 的“不存在”一致。
- hash 存在但为空：返回 `{}`。

Redis 映射：

```redis
EXISTS <hashKey>
HGETALL <hashKey>
```

实现可先 `HGETALL`，如果返回空再 `EXISTS` 区分不存在与空 hash。

#### `share.hash_del(key, field) -> ok, err`

删除 hash 字段。

Redis 映射：

```redis
HDEL <hashKey> <field>
```

#### `share.hash_incr(key, field [, delta [, ttlSec]]) -> value, err`

原子递增 hash 字段。

```lua
local joined, err = share.hash_incr("rank:team:100", "joinedCount")
```

- 字段值必须是 Redis integer。
- 与 `hash_set` 存 JSON 的字段不要混用。
- `delta` 默认 1。
- `ttlSec` 可选。

Redis 映射：

```redis
HINCRBY <hashKey> <field> <delta>
EXPIRE <hashKey> <ttlSec>           -- 仅 ttlSec 存在时
SADD <indexKey> <hashKey>
```

#### `share.hash_expire(key, ttlSec) -> ok, err`

刷新 hash key 的 TTL。

Redis 映射：

```redis
EXPIRE <hashKey> <ttlSec>
```

## 8. 为什么不是完整 Redis 客户端

该能力表面上与 Redis 客户端相似，但边界不同：

| 维度 | share 模块 | Redis 客户端 |
|---|---|---|
| key 命名空间 | 自动加 runId，不能越界 | 用户完全控制 key |
| 命令范围 | 只开放受控共享原语 | 任意 Redis 命令 |
| 危险命令 | 不开放 `KEYS` / `FLUSHDB` / 任意 `EVAL` | 可能开放 |
| 值编码 | 统一 JSON 与 Lua 值转换 | 用户自行处理 |
| 任务清理 | 自动维护 key 索引并清理 | 用户自行清理 |
| 后端抽象 | 面向 stressbot shared state | 面向 Redis 数据库 |
| 错误语义 | 统一 `value, err` | 取决于客户端 |

因此它是 Redis-backed shared state，而不是 Redis Lua 客户端。

## 9. 配置设计

### 9.1 Standalone 模式

`conf/config.json` 顶层新增：

```json
{
  "shared": {
    "redis": {
      "addr": "127.0.0.1:6379",
      "username": "",
      "password": "",
      "db": 0,
      "keyPrefix": "stressbot",
      "defaultClaimTTL": "30s",
      "opTimeout": "2s",
      "dialTimeout": "5s",
      "readTimeout": "2s",
      "writeTimeout": "2s",
      "poolSize": 0
    }
  }
}
```

规则：

- `shared.redis.addr` 为空表示不启用 shared state。
- 脚本调用 `share.*` 时，如果未启用，返回错误：`shared state 未启用，请配置 shared.redis.addr`。
- 不影响未使用 `share` 模块的流程。

### 9.2 Admin / Agent 分布式模式

Redis 连接信息应由 Admin 统一配置，不从前端任务表单直接输入密码，避免将密钥暴露到浏览器、历史记录或导出的任务配置中。

`conf/admin-config.json` 新增：

```json
{
  "shared": {
    "redis": {
      "addr": "127.0.0.1:6379",
      "username": "",
      "password": "",
      "db": 0,
      "keyPrefix": "stressbot",
      "defaultClaimTTL": "30s",
      "opTimeout": "2s",
      "dialTimeout": "5s",
      "readTimeout": "2s",
      "writeTimeout": "2s",
      "poolSize": 0
    }
  }
}
```

任务启动时（**自动识别，不需要前端勾选开关**）：

1. 前端不传"启用共享状态"开关，也没有 `shared.enabled` 字段。一个任务是否需要共享状态，由其 Lua 脚本是否使用 `share` 模块决定。
2. **Admin 权威判定**：任务启动时扫描上传的 Lua 脚本（`task.Config.LuaScripts`，必要时含 `AdapterScript`）是否出现 `require("share")` / `share.` 调用：
   - 用了但 Admin 未配置 Redis → 拒绝启动任务并返回明确中文错误。
   - 用了且配置了 Redis → 生成 runId（= task.ID）、把已解析 Redis 配置注入下发给每个 Agent。
   - 没用 → 不创建 store、跳过 Redis 预检，Redis 是否可用都不影响该任务（不会因 Redis 挂了而误拒绝无关任务）。
3. Agent 不需要在本地配置 Redis；以 Admin 下发的任务配置为准。

> 为什么不用前端开关：一个任务用不用共享状态是脚本里 `require("share")` 已经写明的事实，再让用户手动勾一遍只会产生"用了忘勾→运行时报错""勾了没用→空操作"两类不一致。真正的硬闸门是 Admin 有没有配 Redis，前端开关并不增加安全性。

### 9.3 前端任务配置

**不新增任何任务级共享状态配置字段**（不需要 `RobotConfig.shared`）。前端的职责仅是：

- 静态扫描流程脚本是否出现 `require("share")`，用于展示"该流程使用共享状态"徽标（见 14.3 / 14.4）。
- 结合 9.4 的能力接口，在脚本用到 share 但服务器未配置 Redis 时，于启动按钮处提前预警。

前端不承载 Redis 密码，也不承载启用开关。

### 9.4 Admin 能力状态接口

建议增加只读接口（注意：Admin 路由前缀统一是 `/sbot/`，不是 `/api/`，见 `admin/handlers.go` 现有路由如 `GET /sbot/metrics`）：

```http
GET /sbot/capabilities
```

返回示例：

```json
{
  "sharedState": {
    "enabled": true,
    "backend": "redis",
    "addrMasked": "127.0.0.1:6379",
    "keyPrefix": "stressbot"
  }
}
```

前端用于在启动配置中展示“共享状态可用/不可用”，但不展示密码。

如果不想新增接口，也可以在任务启动失败时提示；但新增能力接口能让 UI 提前校验，体验更好。

## 10. Go 数据结构建议

### 10.1 配置

```go
package sharedstate

type Config struct {
    Redis RedisConfig `json:"redis"`
}

type RedisConfig struct {
    Addr            string `json:"addr"`
    Username        string `json:"username"`
    Password        string `json:"password"`
    DB              int    `json:"db"`
    KeyPrefix       string `json:"keyPrefix"`
    DefaultClaimTTL string `json:"defaultClaimTTL"`
    OpTimeout       string `json:"opTimeout"`
    DialTimeout     string `json:"dialTimeout"`
    ReadTimeout     string `json:"readTimeout"`
    WriteTimeout    string `json:"writeTimeout"`
    PoolSize        int    `json:"poolSize"`
}

type ResolvedRedisConfig struct {
    Addr            string
    Username        string
    Password        string
    DB              int
    KeyPrefix       string
    DefaultClaimTTL time.Duration
    OpTimeout       time.Duration
    DialTimeout     time.Duration
    ReadTimeout     time.Duration
    WriteTimeout    time.Duration
    PoolSize        int
}
```

默认值：

| 字段 | 默认值 |
|---|---|
| `keyPrefix` | `stressbot` |
| `defaultClaimTTL` | `30s` |
| `opTimeout` | `2s` |
| `dialTimeout` | `5s` |
| `readTimeout` | `2s` |
| `writeTimeout` | `2s` |
| `poolSize` | 0，交给 go-redis 默认 |

### 10.2 Store 接口

```go
type Store interface {
    Set(ctx context.Context, key string, value any, ttl time.Duration) error
    Get(ctx context.Context, key string) (any, bool, error)
    Delete(ctx context.Context, key string) error
    Exists(ctx context.Context, key string) (bool, error)
    Expire(ctx context.Context, key string, ttl time.Duration) (bool, error)

    Incr(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error)

    Claim(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error)
    Release(ctx context.Context, key string, owner string) (bool, error)
    Owner(ctx context.Context, key string) (string, bool, error)
    Renew(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error)

    QueuePush(ctx context.Context, key string, value any, ttl time.Duration) error
    QueuePop(ctx context.Context, key string) (any, bool, error)
    QueueLen(ctx context.Context, key string) (int64, error)
    QueueExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)

    HashSet(ctx context.Context, key string, field string, value any, ttl time.Duration) error
    HashGet(ctx context.Context, key string, field string) (any, bool, error)
    HashGetAll(ctx context.Context, key string) (map[string]any, bool, error)
    HashDelete(ctx context.Context, key string, field string) error
    HashIncr(ctx context.Context, key string, field string, delta int64, ttl time.Duration) (int64, error)
    HashExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)

    Cleanup(ctx context.Context) error
    Close() error
}
```

`ttl == 0` 表示不设置过期。`Claim` 入口处由 API 或 Store 用默认 claim TTL 填充，避免永久 claim。

### 10.3 Redis client 依赖

建议使用：

```text
github.com/redis/go-redis/v9
```

需要更新 `go.mod` / `go.sum`。

## 11. JSON 编码与 Lua 值转换

### 11.1 编码范围

支持：

- nil
- bool
- number
- string
- array table
- map table

不支持：

- function
- thread
- userdata，除非未来明确支持 proto message 编码

如果传入不支持类型，返回错误。

### 11.2 数字精度

当前 gopher-lua 的 number 是双精度浮点。超大 int64 在 Lua 和 JSON 之间可能损失精度。设计约定：

- 普通数值、计数器可用 number。
- `teamId`、`roleId` 等可能超过 2^53 的 ID，建议用 string 存储。

示例：

```lua
share.hash_set("rank:team:100", "teamId", tostring(robot.get("teamId")))
```

### 11.3 JSON 解码

Go 解码 Redis JSON 时使用 `json.Decoder.UseNumber`，避免 Go 侧过早转为 float64。转回 Lua 时：

- 可安全转 number 的数值转 Lua number。
- 无法安全表达的大整数建议保留 string；但由于 JSON 中无法知道用户原意，文档要求用户主动字符串化大 ID。

### 11.4 与现有 Lua 值转换保持一致

`script/api_robot.go` 已有 `goValueToLua` / `luaToGoValue`，且对 `int64`/`uint64` 超过 `maxSafeInt`（2^53）的值会自动转字符串（见 `goValueToLua`）。`sharedstate/codec.go` 是一套独立编解码（操作 `any` ↔ JSON），必须采用**同样的大整数→字符串策略**，否则 `share.get` 读出的大整数与 `robot.get` 行为不一致，用户会困惑。

实现建议：codec 的 "JSON → Lua 可用 Go 值" 这一步直接复用 / 对齐 `goValueToLua` 的阈值逻辑（≥2^53 转 string），并在 `codec_test.go` 中加一条断言固化该行为。

### 11.5 counter 类返回值的精度

`share.incr` / `share.hash_incr` 底层是 Redis `INCRBY` / `HINCRBY`，返回 `int64`。Store 接口返回 `int64`，但 `api_share.go` 推回 Lua 时是 `lua.LNumber`（double），当计数器累计超过 2^53 会丢精度。

约定：

- 普通计数器（队伍人数、加入计数等远小于 2^53）直接返回 number，无影响。
- 文档需提醒：不要把 counter 当作超大 ID 累加器使用；超大 ID 仍按 11.2 用字符串 KV 存储。

## 12. 错误处理

### 12.1 Lua 返回错误

所有 `share` API 返回 `err` 字符串，不直接 raise error。

示例：

```lua
local team, err = share.get("rank:team:100")
if err ~= nil then
    log.error("读取共享队伍失败: " .. err)
    return -1
end
```

### 12.2 未启用 shared state

如果 `Context.Shared == nil`：

```lua
local v, err = share.get("x")
-- v == nil
-- err == "shared state 未启用，请配置 Redis"
```

### 12.3 Redis 不可用

Redis 操作超时、连接失败、认证失败时：

- API 返回错误字符串。
- Go 层打中文 warn/error 日志，包含操作类型、逻辑 key、runId、错误原因。
- 不打印 Redis password。

### 12.4 脚本是否失败由用户决定

`share` API 只返回错误，不自动让 action 失败。脚本必须显式：

```lua
if err ~= nil then return -1 end
```

这样保持与现有 Lua 网络 API “返回 code，由脚本判断”的风格一致。

## 13. 任务生命周期

### 13.1 启动

Standalone：

1. 读取 `conf/config.json` 的 shared.redis。
2. 如果 `addr` 非空，创建 `RedisStore`。
3. 生成 runId。
4. Manager 创建 Robot 时注入 RedisStore。

Admin / Agent：

1. Admin 启动时读取 Redis 配置。
2. **Admin 扫描任务脚本是否使用 `share`（`require("share")`）来自动判定该任务是否需要共享状态**，无需用户手动开关。
3. 若使用 share：Admin 校验 Redis 配置存在且可用，**生成一个任务级 runId（直接使用 task.ID）**；若未使用 share：跳过 store 创建与 Redis 预检。
4. 使用 share 时，Admin 将内部 shared runtime 配置（含 runId + 已解析的 Redis 连接信息）随任务一并下发给**每一个** Agent（写入 `TaskAssignment`）。
5. **同一任务的所有 Agent 使用同一个 runId**，因此共享同一命名空间和同一 key 索引集合，天然实现跨 Agent 协作。
6. 每个 Agent 的 TaskRunner 创建自己的 `RedisStore`（同 runId、同 prefix），该 Agent 内所有 Robot 共享该实例。

> 关键：runId 必须由 Admin 统一生成并下发，不能由各 Agent 各自生成，否则不同 Agent 落在不同命名空间，无法协作。

### 13.2 运行

- 每次写入共享 key 时，RedisStore 自动 `SADD` 到索引集合。
- 普通 key TTL 可选。
- Claim 不传 TTL 时使用默认 claim TTL。
- Queue/Hash 的 TTL 作用于整个 Redis key。

### 13.3 正常停止与清理

清理（删除该 runId 下所有共享 key + 索引集合）和连接关闭（`Close`）是两件事，按模式区分触发点：

#### 单机模式

由 `cmd/agent/main.go` 的 `runStandalone` 在收尾处统一执行（Ctrl+C 信号和 duration 到期两条路径汇合的同一段尾部，`mgr.StopAll()` 之后）：

```go
mgr.StopAll()
if sharedStore != nil {
    sharedStore.Cleanup(cleanupCtx) // 短超时
    sharedStore.Close()
}
```

#### 分布式模式（关键）

分布式下一个任务由 N 个 Agent 执行，**它们共享同一个 runId 和同一个 key 索引集合**。如果每个 Agent 各自在 TaskRunner 结束时 `Cleanup`，最先结束的 Agent 会 `SMEMBERS + DEL` 掉整个共享空间，而其他还在跑流程的 Agent 会突然读到队伍/slot 消失。因此：

- **Agent 侧 TaskRunner 结束时只 `Close()`（释放连接），绝不 `Cleanup()`。**
- **Cleanup 由 Admin 统一触发一次**：用任务的 runId（= task.ID）+ Admin 自身的 Redis 配置创建一个临时 cleanup-only Store（或复用 Admin 持有的 Store），执行一次 `Cleanup`。
- **具体挂载点：`admin` 的终态回调 `onTaskTerminal(task)`（经 `TaskStore.SetOnTerminal` 注册）。** 该回调已是所有终态转换的唯一汇聚点（现用于停止 Sampler + 异步归档，见 `admin/admin.go`），在这里追加一次 shared cleanup 即可，不引入新状态机。
- 该回调对**所有终态路径**都生效：正常收齐所有 Agent 报告转 stopped、stopWaitTimeout 合成 fake report 转 stopped、失败转 failed、以及 Admin 重启恢复任务（`SetOnTerminal` 会对 recoveredIDs 补触发）。因此即便部分 Agent 离线/崩溃没上报，清理也不会漏。
- 注意：`onTaskTerminal` 收到的是 Task 的深拷贝（JSON 序列化），`task.ID`（runId）与 `task.Config.LuaScripts`（用于重新判定该任务是否用到 share）都在拷贝里；Redis 连接信息从 Admin 配置读取（不依赖拷贝，拷贝里也不含密码）。

#### 清理失败处理（两种模式通用）

- 打 warn/error 中文日志（含 runId、删除的 key 数、错误原因），不打印 Redis password。
- 使用短 timeout（例如 5s 或配置项），不阻塞任务停止 / 终态流转过久。
- 清理失败不影响任务状态判定；残留 key 由 runId 隔离 + TTL + 运维按 prefix 清理兜底。

### 13.4 异常退出

进程崩溃时无法执行 Cleanup。影响：

- 带 TTL 的 key 自动过期。
- 未带 TTL 的 key 会残留，但带 runId，不污染新任务。
- Redis 管理员可按 prefix 清理。

如果希望减少异常残留，可配置脚本显式 TTL，或未来增加离线清理命令。

## 14. 前端改动范围

### 14.1 Lua API 文档

文件：

```text
cmd/web/src/components/FlowEditor/lua/luaApiSpec.ts
cmd/web/src/components/FlowEditor/lua/__tests__/luaApiSpec.test.ts
cmd/web/src/components/FlowEditor/lua/LuaApiPopover.tsx
```

改动：

1. 新增 `share` 模块函数清单。
2. `MODULE_COLOR` 增加 `share` 颜色，例如 `red` 或 `volcano`。
3. 测试中加入 `share` 模块导出校验，避免文档漏函数。
4. 函数签名展示可选参数，如 `[ttlSec]`。
5. 文案说明：`share` 需要任务启用 Redis 共享状态。

### 14.2 Lua 编辑器提示 / 自动补全

如果现有 LuaForm 基于 `luaApiSpec.ts` 生成提示，则自动覆盖。若有独立补全列表，需要同步添加：

```lua
local share = require("share")
share.set
share.get
share.claim
share.queue_push
share.hash_set
```

### 14.3 脚本静态扫描与校验

需要让前端识别脚本中使用了 share 模块：

```lua
local share = require("share")
```

校验建议：

- 如果 flow 使用 `share`，但任务配置未开启共享状态，校验报告给出错误或强警告。
- 如果任务配置开启共享状态，但服务器能力接口显示 Redis 不可用，启动按钮处给出错误提示。
- 如果只是在编辑 flow，无法得知 Admin 配置时，提示“该流程需要 Redis 共享状态”。

涉及可能文件：

```text
cmd/web/src/components/FlowEditor/lua/luaApiSpec.ts
cmd/web/src/components/FlowEditor/lua/__tests__/luaApiSpec.test.ts
cmd/web/src/components/FlowEditor/editors/ActionEditor/LuaForm.tsx
cmd/web/src/components/FlowEditor/editors/ActionEditor/stateRegistry.ts
cmd/web/src/components/FlowEditor/validation/* 或现有校验入口
```

具体以现有校验结构为准。

### 14.4 任务启动配置 UI

涉及：

```text
cmd/web/src/types/api.ts
cmd/web/src/services/runtimeStore.ts
cmd/web/src/components/runtime/RuntimeBar.tsx
cmd/web/src/services/taskActions.ts
cmd/web/src/services/index.ts
```

**不提供"启用共享状态"开关**（是否启用由脚本是否使用 `share` 自动判定，见 9.2 / 9.3）。前端在启动配置区只做"展示 + 预警"：

- 若静态扫描检测到流程脚本使用 `share`，展示一个只读徽标/提示，告知该流程使用共享状态。
- 结合 capabilities（9.4），若使用 share 但服务器未配置 Redis，则在启动按钮处给出错误提示，阻止启动。
- 不输入、不展示 Redis 密码。

UI 文本遵守项目约定（Agent→节点、Admin→服务器）。面向用户的文案示例：

```text
该流程使用共享状态，允许多个机器人协作（组队、队伍池、任务分片等）。
共享状态未配置，请先在服务器配置中启用 Redis。
```

### 14.5 API 类型

**不新增 `RobotConfig.shared` / `SharedRuntimeConfig`**（无任务级开关）。仅需能力接口响应类型：

`cmd/web/src/types/api.ts`：

```ts
export interface CapabilitiesResponse {
  sharedState: {
    enabled: boolean;     // 服务器是否已配置 Redis（能力可用），非任务级开关
    backend?: 'redis';
    addrMasked?: string;
    keyPrefix?: string;
  };
}
```

`cmd/web/src/services/*`：

- 新增 `fetchCapabilities()` 或放入现有 service index。
- 启动任务前：若检测到脚本使用 share 且 `sharedState.enabled === false`，给出错误提示。

### 14.6 历史与摘要

历史记录和任务摘要不保存 Redis 密码。"是否使用共享状态"是从脚本派生的事实，不再是用户配置项；如需在摘要中体现，可在归档时由 Admin 写入一个**派生**布尔值（可选，非必需）：

```json
{
  "sharedUsed": true,
  "sharedBackend": "redis"
}
```

`ConfigSummary` 可选增加（派生字段，来源于脚本扫描结果，而非用户输入）：

```ts
sharedUsed?: boolean;
sharedBackend?: 'redis';
```

## 15. 后端改动范围

### 15.1 新增 sharedstate 包

新增文件：

```text
sharedstate/config.go
sharedstate/store.go
sharedstate/redis_store.go
sharedstate/codec.go
sharedstate/scripts.go
sharedstate/redis_store_test.go
sharedstate/codec_test.go
```

### 15.2 script 包

改动：

```text
script/runtime.go
script/api_share.go
script/lua_api_spec_docs 如果存在
```

`registerAPIs` 新增：

```go
L.PreloadModule("share", loadShareModule)
```

`Context` 新增：

```go
Shared sharedstate.Store
```

`api_share.go` 实现 Lua 参数解析、调用 Store、返回 Lua 双返回值。

### 15.3 robot 包

改动：

```text
robot/robot.go
robot/manager.go
```

`robot.Config` 新增：

```go
Shared sharedstate.Store
```

`ManagerConfig` 新增：

```go
Shared sharedstate.Store
```

`Manager.startBatch` 创建 Robot 时传入；`Robot.Start()` 构造 `script.Context` 时写入 `Shared`。

`script.Context` 在 robot 包里有**两个**构造点，都要写入 `Shared`（否则监听回调脚本里的 `share.*` 会拿到"未启用"错误）：

1. `Robot.Start()` —— action / boolean 脚本路径。
2. `createListenCallback()` 回调闭包 —— 监听回调脚本路径。

生命周期约束（重要）：

- Robot 关闭**不** Close shared store，因为同一 Agent 内多个 Robot 共享同一实例。
- **shared store 的 `Cleanup`/`Close` 绝不能挂在 `Manager.StopAll` 或 ramp-up 的 `resetBots` 路径上**。`StartWithRampUp` 的 reset 阶段会反复 `resetBots()` 销毁重建机器人；若清理与 Manager 停止绑定，多阶段任务在中途 reset 时就会误清空共享状态。
- shared store 的 `Close` 由上层编排负责：单机在 `cmd/agent/main.go` 收尾、分布式在 `agent/task_runner.go` 收尾；`Cleanup` 见 13.3（单机本地执行，分布式由 Admin 统一触发）。Manager 只负责把同一个 store 透传给每个 Robot。

### 15.4 standalone 启动

改动：

```text
cmd/agent/main.go
```

在 `cmd/agent/main.go` 的顶层 `Config` 结构新增 `Shared *sharedstate.Config`（与 `Standalone` 同级）。`runStandalone` 启动 Manager 前：当 `shared.redis.addr` 非空且预编译脚本中检测到使用 `share`（`luaPool` 已加载脚本，可扫描脚本源或维护一个"是否用到 share"标记）时，创建 RedisStore 并注入 `ManagerConfig.Shared`；否则不创建。runId 用 `standalone-<unixMilli>-<pid>`（见 6.2）。

> 单机模式同样遵循"自动识别"口径：没用到 `share` 就不建 store；配了 `addr` 但脚本没用也不会强行连 Redis。

清理/关闭统一放在 `runStandalone` 收尾段（`mgr.StopAll()` 之后）：Ctrl+C 信号与 duration 到期两条路径汇合于此，单点调用一次 `Cleanup` + `Close` 即可覆盖所有正常退出路径。错误退出（panic / Fatal）走 13.4 异常退出兜底（TTL + runId 隔离）。

### 15.5 Agent / Admin 分布式

涉及：

```text
admin/config.go        // 新增 Shared 配置段（RedisConfig）
admin/types.go         // TaskAssignment 新增内部 shared runtime 字段（无 RobotConfig 开关）
admin/handlers.go      // 启动任务时扫描脚本判定 + 构造 TaskAssignment 注入 runId+Redis；capabilities 接口
admin/admin.go         // onTaskTerminal 内追加统一 Cleanup（SetOnTerminal 已注册的终态回调）
agent/task_runner.go   // assignment.Shared != nil 时创建 RedisStore，结束时只 Close
agent/types.go         // TaskAssignment 对应字段（与 admin/types.go 同步）
```

`TaskAssignment` 新增内部字段（仅 Admin→Agent 传输，不回前端、不入历史归档）：

```go
// 任务级共享状态运行时配置（Admin 注入，Agent 直接使用，不再读本地 Redis 配置）。
Shared *SharedRuntimeAssignment `json:"shared,omitempty"`

type SharedRuntimeAssignment struct {
    RunID string       `json:"runId"` // = task.ID，同任务所有 Agent 一致
    Redis RedisConfig  `json:"redis"` // 已解析的 Redis 连接信息（含 password）
}
```

> Redis password 会随 Admin→Agent 的任务下发 RPC 传输并驻留在 Agent 内存——这是分布式注入的必要代价。约束：不写前端、不入历史归档、日志不打印（见第 17 章）。

设计：

- Admin 读取 Redis 配置（`admin-config.json` 的 `shared.redis`）。
- Admin 提供 capabilities（见 9.4，路径 `GET /sbot/capabilities`）。
- **Admin 启动任务时扫描脚本是否使用 `share` 自动判定**（见 9.2）：使用 share 但未配置 Redis → 拒绝启动并返回明确中文错误；未使用 → 不下发 shared 配置、跳过预检。
- 使用 share 时，Admin 用 `task.ID` 作为 runId，连同已解析的 Redis 配置写入每个 `TaskAssignment.Shared` 下发。
- Agent TaskRunner 在 `assignment.Shared != nil` 时创建 RedisStore（同 runId、同 prefix），注入 `ManagerConfig.Shared`；为 nil 时不创建。
- **Agent TaskRunner 结束时只 `Close()`，不 `Cleanup()`**（见 13.3）。
- **Admin 在终态回调 `onTaskTerminal`（`TaskStore.SetOnTerminal` 注册，`admin/admin.go`）里统一执行一次 `Cleanup`**：判定 `task.Config.LuaScripts` 是否用到 share 且 Admin 配了 Redis → 用 runId（= task.ID）+ Admin Redis 配置 new 一个 cleanup-only Store，`Cleanup` 后 `Close`。该回调覆盖收齐报告 / stopWaitTimeout 兜底 / failed / Admin 重启恢复全部终态路径。

### 15.6 错误码

**重要：`share.*` API 的错误通过 Lua 双返回值的 `err`（字符串）暴露，由脚本自行决定是否 `return -1`，并不走 `errcode` 的 `ActionError` 分类链路。** 因此第一版基本用不到新增 `errcode` 常量——直接返回中文错误字符串即可。

只有在你希望让"未启用 / Redis 不可用"在 monitor 的错误聚合面板里**单独成类**时，才有必要：让脚本把某个约定 code `return` 出来，并在 `errcode.codes.go` 注册对应常量（注意：`codeRegistry` 是唯一真理源，新增需同步维护，参见 `errcode/codes.go` 与现有 Lua 层 51-60 段）。可选常量：

```text
ErrSharedConfig          // 未启用 / 配置缺失
ErrSharedRedisUnavailable // 连接/认证/超时
ErrSharedOperationFailed  // 一般操作失败
ErrSharedSerialization    // 值编解码失败
```

结论：第一版默认走字符串 `err`，errcode 为可选增强；不强制新增。

错误信息使用中文。

## 16. Redis 命令与原子性

### 16.1 需要 Lua 脚本封装的操作

为了保证操作和索引记录、TTL 刷新一致，以下 Redis 操作建议用 Redis Lua：

- `incr`: `INCRBY + optional EXPIRE + SADD`
- `queue_push`: `RPUSH + optional EXPIRE + SADD`
- `hash_set`: `HSET + optional EXPIRE + SADD`
- `hash_incr`: `HINCRBY + optional EXPIRE + SADD`
- `release`: owner 匹配才 DEL
- `renew`: owner 匹配才 EXPIRE

### 16.2 可以普通 pipeline 的操作

- `set`: 可用 `SET` 原生命令，再 `SADD`；如果要强一致，也可 Lua。
- `del`: `DEL` 即可。
- `get/exists/pop/len`: 单命令即可。

为了实现简单一致，写入类操作统一走 Lua 脚本也是可行的。

### 16.3 并发与 luaMu（强制约束）

这是 `share` 模块接入运行时最关键、也最容易出事的一点。

Lua 动作执行期间，`robot` 层整段持有 `r.luaMu`（见 `robot/robot.go` 的 `executeLuaAction`：`h.robot.luaMu.Lock(); defer Unlock()`）。而**心跳 Builder、decodeLoop（解码循环）、监听回调都要抢同一把 `luaMu`**。因此现有所有阻塞型 Lua API（`tcp_request` / `udp_request` / `http_request` / `connect_*` / `close_*` / `sleep` / `tcp_listen`）在执行真正的阻塞操作时，**都通过 `withReleasedMu(ctx.LuaMu, fn)` 临时释放 `luaMu`**（见 `script/api_network.go`、`script/api_utils.go`）。

`share.*` 的 Redis 调用是网络往返（`opTimeout` 默认 2s），属于阻塞操作，**必须遵循同一约束**：

```go
// api_share.go 中每个会发起 Redis 往返的函数：
var val any
var ok bool
var err error
withReleasedMu(ctx.LuaMu, func() {
    val, ok, err = ctx.Shared.Get(opCtx, key)
})
```

如果不释放 `luaMu` 就做 Redis 往返：

- 该 Robot 的心跳、解码、监听回调会被卡住，最长每次 Redis 往返时间（默认 2s）。
- 第 18 章那种 30 秒轮询循环（每 200ms 一次 `share.hash_get` + Redis RTT）会把该 Robot 的心跳/解码基本打死，造成连接被服务端判定超时断开、监控大面积假超时。

实现 `api_share.go` 时，逐个函数 review：凡是调用 `ctx.Shared.*`（会触达 Redis）的地方，一律 `withReleasedMu` 包裹，且 `withReleasedMu` 之外不要再访问 Redis。

### 16.4 对监控耗时的影响

即使按 16.3 释放了 `luaMu`，`RunActionScript` 的 `wallClock` 仍然包含 Redis 往返耗时；而 monitor 用 `wallClock - sum(WireRTT)` 计算客户端开销（`ClientAvgMs`）。`share.*` 不进 `ctx.recordRequest`（不是一次 game 协议 request-response），所以：

- **所有 Redis 往返耗时都会被计入该动作的"客户端开销"列（ClientAvgMs）。**
- 启用共享状态的 `lua` 动作，其 ClientAvgMs 会因 Redis RTT 而虚高。

第一版接受这一行为，但需在文档里说明，避免使用者把 share 动作的高 ClientAvgMs 误读为客户端构建/解析慢。如需精确区分，可在未来为 share 操作引入独立 timing 维度（非第一版必需）。

### 16.5 Redis 操作的超时与取消

`share.*` 内部调用 Store 时应使用带超时的 context：

- 基于 `ResolvedRedisConfig.OpTimeout` 派生 `opCtx`。
- 同时关联 `ctx.Ctx`（Robot 生命周期 ctx），使任务停止 / robot.Stop 能尽快取消 inflight 的 Redis 调用，不拖慢清理。

即 `opCtx, cancel := context.WithTimeout(ctx.Ctx, opTimeout)`，避免任务停止时还在等满 2s Redis 超时。

## 17. 安全与隔离

1. Lua 用户只传逻辑 key，不传 Redis 真实 key。
2. Go 层自动加 `prefix/runId/type`。
3. 不开放任意 Redis 命令。
4. 不开放跨 runId 访问。
5. 日志不打印密码。
6. 前端不接收 Redis password。
7. 任务历史不保存 Redis password。
8. Redis DB 与 prefix 由服务器配置控制。

## 18. 排位三排示例

### 18.1 队长发布队伍

```lua
local share = require("share")
local robot = require("robot")
local log = require("log")

function execute(r)
    local teamSize = 3
    local groupId = math.floor(robot.get("index") / teamSize)
    local key = "rank:team:" .. tostring(groupId)

    local info = {
        teamId = tostring(robot.get("teamId")),
        tsId = robot.get("tsId"),
        model = robot.get("model"),
        gType = robot.get("gType"),
        modeId = robot.get("battleModeId"),
        leader = robot.get_account(),
    }

    local ok, err = share.hash_set(key, "info", info, 120)
    if err ~= nil or not ok then
        log.error("发布共享队伍失败: " .. tostring(err))
        return -1
    end

    share.hash_set(key, "status", "recruiting", 120)
    share.hash_incr(key, "joined", 1, 120)
    return 0
end
```

### 18.2 队员等待队伍并抢 slot

```lua
local share = require("share")
local robot = require("robot")
local utils = require("utils")
local log = require("log")

function execute(r)
    local teamSize = 3
    local groupId = math.floor(robot.get("index") / teamSize)
    local role = robot.get("index") % teamSize
    local key = "rank:team:" .. tostring(groupId)
    local deadline = utils.time_ms() + 30000

    while utils.time_ms() < deadline do
        local info, err = share.hash_get(key, "info")
        if err ~= nil then
            log.error("读取共享队伍失败: " .. err)
            return -1
        end
        if info ~= nil then
            local slotKey = key .. ":slot:" .. tostring(role)
            local ok, claimErr = share.claim(slotKey, robot.get_account(), 60)
            if claimErr ~= nil then
                log.error("抢占队伍位置失败: " .. claimErr)
                return -1
            end
            if ok then
                robot.set("joinTeamInfo", info)
                share.hash_incr(key, "joined", 1, 120)
                return 0
            end
        end
        utils.sleep(200)
    end

    log.warn("等待共享队伍超时")
    return -1
end
```

### 18.3 队长等待满员

```lua
local share = require("share")
local robot = require("robot")
local utils = require("utils")

function execute(r)
    local teamSize = 3
    local groupId = math.floor(robot.get("index") / teamSize)
    local key = "rank:team:" .. tostring(groupId)
    local deadline = utils.time_ms() + 30000

    while utils.time_ms() < deadline do
        local joined, err = share.hash_get(key, "joined")
        if err ~= nil then return -1 end
        if joined ~= nil and tonumber(joined) >= teamSize then
            share.hash_set(key, "status", "ready", 120)
            return 0
        end
        utils.sleep(200)
    end
    return -1
end
```

## 19. 测试方案

### 19.1 Go 单元测试

`sharedstate/codec_test.go`：

- Lua/Go 基础类型编码解码。
- table array/map 编码解码。
- 不支持类型返回错误。
- JSON number 行为确认。

`sharedstate/redis_store_test.go`：

需要 Redis。建议测试支持环境变量：

```text
STRESSBOT_REDIS_TEST_ADDR=127.0.0.1:6379
```

未设置时跳过集成测试。

覆盖：

- set/get/del/exists。
- set with TTL 过期。
- incr 默认 delta / 指定 delta / TTL 刷新。
- claim 成功 / 重复 claim 失败 / release owner 匹配 / owner 不匹配。
- renew owner 匹配 / owner 不匹配。
- queue FIFO。
- queue len。
- hash set/get/get_all/del/incr。
- Cleanup 删除索引集合中的 key。
- runId 隔离：两个 store 同 logical key 不互相影响。
- **多 Agent 清理语义**：两个 store 共用同一 runId 写入后，只触发一次 `Cleanup` 即清空共享空间；模拟"Agent A 先结束不清理、Agent B 仍可读"——即验证 Agent 侧只 Close 不 Cleanup 的设计（Cleanup 仅由单一入口执行）。

`script/api_share_test.go`：

- 未配置 Shared 返回错误。
- Lua `require("share")` 可加载。
- 各 API 参数错误返回 Lua err。
- API 返回双返回值。
- **luaMu 释放回归**：构造一个持有 luaMu 的场景，调用阻塞型 `share.*`（可用 mock/慢 Store），断言期间另一 goroutine 能拿到 luaMu（即 `withReleasedMu` 生效），防止回归成"持锁阻塞心跳"。
- 监听回调路径下 `share.*` 可用（验证 `createListenCallback` 的 Context 也注入了 Shared）。

### 19.2 前端测试

`cmd/web/src/components/FlowEditor/lua/__tests__/luaApiSpec.test.ts`：

- `share` 模块存在。
- share 模块函数数量和签名符合预期。
- `renderSignature` 正确展示可选参数。

如果已有 flow 校验测试：

- 脚本包含 `require("share")` 时能识别共享状态依赖。
- 未启用 shared 配置时给出提示。

### 19.3 构建验证

按项目验证流程：

```bash
go build ./...
cd cmd/web && npx tsc -b
cd cmd/web && npm run test
```

涉及 Redis 集成测试时：

```bash
$env:STRESSBOT_REDIS_TEST_ADDR = "127.0.0.1:6379"
go test ./sharedstate ./script
```

Windows PowerShell 下环境变量按项目实际命令调整。

## 20. 实施顺序建议

1. 新增 `sharedstate` 包和 RedisStore（含 codec 大整数一致性、`Cleanup` 用 `SSCAN` + 分批 `DEL`）。
2. 接入 `script.Context`（**两个构造点**）和 `share` Lua 模块；`api_share.go` 所有 Redis 调用 **`withReleasedMu` 包裹** + `OpTimeout` 关联 `ctx.Ctx`。
3. 接入 standalone Manager/Robot 生命周期：`ManagerConfig.Shared` 透传；`Cleanup`/`Close` 放 `cmd/agent/main.go` 收尾，**不绑 Manager/reset**。
4. 接入 Admin 配置、Agent task 下发（`TaskAssignment.Shared` 含 runId+Redis）、TaskRunner 生命周期（**Agent 只 Close**）。
5. Admin 在任务终态聚合处**统一触发一次 `Cleanup`**（收齐所有报告或 stopped/failed/兜底）。
6. 增加 `GET /sbot/capabilities` 接口。
7. 更新前端 API 类型、RuntimeStore 默认值、任务启动 UI。
8. 更新 Lua API 文档、Popover、测试。
9. 增加 flow 校验提示。
10. 补充示例 Lua 脚本或文档片段。
11. 完整验证（含 luaMu 释放回归、多 Agent 清理语义、心跳不被 share 阻塞）。

## 21. 风险与缓解

| 风险 | 缓解 |
|---|---|
| Redis 不可用导致复杂流程失败 | share API 返回 err，脚本显式处理；任务启动时可预检 Redis |
| 未设置 TTL 的 key 在异常退出后残留 | runId 隔离；正常停止清理；必要时用户给 TTL；运维可按 prefix 清理 |
| claim 租约过短导致 slot 被重复抢 | 支持 `share.renew`；默认 TTL 可配置；脚本长流程中定期续租 |
| 大整数精度丢失 | 文档要求大 ID 字符串化；示例使用 tostring(teamId) |
| Redis key 过多 | 任务结束批量清理；队列/hash 场景优先使用结构化 key；监控 Redis 内存 |
| 前端暴露 Redis 密码 | Redis 配置只在服务器配置；前端只看到能力状态 |
| API 退化成完整 Redis 客户端 | 只开放受控原语，不提供任意命令 |
| 清理时 DEL 参数过多 | 分批删除；超大集合用 `SSCAN` 迭代 |
| 脚本忽略 err | 文档和示例强调必须检查 err；校验可提示 share 调用未接 err 但不强制 |
| **share.\* 持 luaMu 阻塞心跳/解码/回调** | **强制 `withReleasedMu` 释放锁后再访问 Redis（见 16.3）；`OpTimeout` 关联 robot ctx 可快速取消** |
| **多 Agent 共享 runId 时清理竞态**（先结束的 Agent 误删仍在用的共享 key） | **Agent 侧只 Close 不 Cleanup；Cleanup 由 Admin 收齐所有报告/终态后统一触发一次（见 13.3、15.5）** |
| **监听回调脚本里 share.\* 拿到"未启用"** | **`createListenCallback` 的 Context 也注入 `Shared`（两个构造点都要写）** |
| Redis RTT 计入动作 ClientAvgMs，监控误读 | 文档说明 share 操作耗时会进客户端开销列（见 16.4）；如需精确区分后续加独立 timing |
| Admin→Agent 下发携带 Redis password | 仅内部 RPC 传输；不回前端、不入历史归档、日志不打印（见 17 章） |

## 22. 文档更新清单

需要同步更新：

- `CLAUDE.md`：Lua 模块数量、API 说明、配置文件 shared.redis 段。
- `plans/api-monitor.md` 或当前 API 文档：新增 `/sbot/capabilities`（共享状态由脚本是否使用 `share` 自动判定，无 RobotConfig 开关字段）。
- 前端 `luaApiSpec.ts`：share API。
- 配置示例：`conf/config.json`、`conf/admin-config.json` 可加入注释性示例或文档说明。
- 如有 README 的配置章节，也应补充 Redis shared state 用法。

## 23. 最终推荐

采用 Redis-only shared state：

```lua
local share = require("share")
```

第一版支持：

```lua
-- KV
share.set / get / del / exists / expire

-- Counter
share.incr

-- Claim
share.claim / release / owner / renew

-- Queue
share.queue_push / queue_pop / queue_len / queue_expire

-- Hash
share.hash_set / hash_get / hash_get_all / hash_del / hash_incr / hash_expire
```

底层采用：

```text
任务命名空间 + Redis 原生类型 + key 索引集合清理
```

不采用：

```text
单大 Hash 存所有状态
进程内 memory 后端
完整 Redis 客户端
业务专用组队接口
```

这样既能一步到位支持跨进程、跨 Agent 协作，又保持 stressbot 引擎层的通用性和任务隔离。