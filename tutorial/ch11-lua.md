# 第 11 章 Lua：声明式表达不了的行为

> **目标**：知道什么时候**不该**用 Lua，以及必须用的时候怎么写对。脚本能力覆盖：按复杂数据决策、动态协议交互（密钥交换）、条件求值、推送回调。
>
> **前置**：第 5~9 章。示例 `examples/ch11/`：`smart_buy.lua`（按背包剩余格子决定购买数量）+ `check_ready.lua`（lua: 条件）。

## 11.1 决策树：用不用 Lua

```
这个行为能用绑定 + 条件 + 节点表达吗？
 ├─ 能 ──────────────► 用声明式（可校验、可视化、免脚本调试）
 └─ 不能。是哪类？
     ├─ 单个取值/过滤太复杂 ──► 再看一眼第 7 章的 17 种绑定和 12 个过滤器
     ├─ 要读大消息做复杂计算 ──► Lua + get_view（只读挑着看）
     ├─ 协议交互本身动态（密钥交换、握手多步）──► Lua + network
     └─ 推送到达要立刻响应 ──► listen 的 script 回调（on_message）
```

Lua 动作没有加载期的字段级校验、没有可视化编辑优势——每个 Lua 动作都是"一小段不可见的行为"，能声明式就声明式。

## 11.2 脚本契约

```lua
local robot = require("robot")     -- 七个内置模块：robot/proto/network/utils/log/json/share

function execute(r)
    -- return nil              → 成功
    -- return robot.error(code, detail)  → 失败（err table）
end
```

三条铁律（都有运行期强制）：

1. `execute` 必须是**全局函数**，否则报 `脚本 xxx 未定义 execute 函数`；
2. 返回值只有两种合法形态：`nil` 或 `robot.error(code, detail)` 构造的表。返回数字等旧式写法直接报错；
3. `robot.error` 只是**构造**错误表，不中断脚本——必须 `return robot.error(...)`。code 填业务码（≥100）或复用框架码语义。

脚本运行失败的错误码：未定义入口 52、执行失败 53、Lua 未初始化 51。

## 11.3 robot 模块：状态读写

| 签名 | 语义 |
|---|---|
| `robot.get(key)` | 整读成**独立 Lua 表**（整树物化，成本 ∝ 数据大小）；随便改，不影响 state |
| `robot.get_view(key)` | 借**只读惰性视图**（userdata，零物化）；大消息只挑着看用它 |
| `robot.set(key, v)` / `robot.set_path("a.b[0].c", v)` | 写状态（表会深拷贝固化） |
| `robot.get_path("a.b[0].c")` | 按路径读 |
| `robot.get_id()` / `robot.get_index()` / `robot.get_account()` | 机器人身份三件套 |
| `robot.clear([key])` / `robot.delete(key)` | 清空/删键——身份键拒绝删除（warn 忽略） |

**get vs get_view 一句话**：整份数据要拿来自由加工 → `get`；大消息只读挑着看 → `get_view`。两者形态互斥不会混用出错：视图上用表语法（`view.foo`）会得到指路报错；`get_view` 用在标量/脚本存的表/被 set_path 改过的 key 上也会报错指回 `get`。

## 11.4 proto 模块：操作消息与视图

| 签名 | 语义 |
|---|---|
| `proto.create("arena.BuyItemC2S")` | 创建消息 userdata |
| `proto.set_field(msg, "itemId", v)` | 写字段（视图/Frozen 禁止改写） |
| `proto.get_field(msg, "fieldName")` / `proto.get_path` | 读单字段（两者同一实现，字段名直传，**不支持带下标路径**） |
| `proto.list_size(msg, "items")` / `proto.list_get(msg, "items", i)` | 列表长度 / 取第 i 个（**1-based**；message 元素返回子视图） |
| `proto.iter_list(msg, "items")` | 顺序遍历整列表（O(n)，替代逐下标 O(n²)） |
| `proto.serialize(msg)` | 序列化成 string（视图直接返回原始字节） |

下钻带下标的路径（`a.b[0].c`）是 **robot.get_path（state 侧）** 的能力，proto 模块不提供——别混。

## 11.5 network 模块：脚本里的协议交互

| 签名 | 返回 |
|---|---|
| `network.connect_tcp(service, address)` | err |
| `network.close_tcp(service)` | — |
| `network.tcp_request(service, route, msg [, s2cProto [, timeoutSec]])` | err, data |
| `network.tcp_send(service, route, msg)` | err |
| `network.tcp_listen(service, route [, s2cProto [, timeoutSec]])` | err, data |
| `network.ensure_tcp_listener(service, routeKey)` | 预注册监听占位 |
| `network.set_tcp_secret_key(service, key)` / `get_tcp_secret_key(service)` | 设置/读密钥 |

最容易错的三个点（都是实测/源码核实）：

1. **没有 `await_listen` 这个名字**——等待版就是 `network.tcp_listen`；
2. `err` 是 **table**（`err.code` / `err.detail`），不是字符串。成功 `err == nil`；服务端业务错误的 code 就是服务器错误码（≥100），框架失败是 1~53；`data` 配了 s2cProto 是消息 userdata（大消息给只读视图），没配是原始 string；
3. **`tcp_listen` 不会自动注册监听**——推送到达前必须先 `ensure_tcp_listener(service, routeKey)`（占位队列容量 1），否则等满超时（code 31）。

示例（本章 `smart_buy.lua` 的核心段）：

```lua
local msg = proto.create("arena.ShopListC2S")
proto.set_field(msg, "category", 0)
local err, data = network.tcp_request("arena", { cmd = 500, act = 1 }, msg, "arena.ShopListS2C", 5)
if err ~= nil then return err end
```

route 用 Lua 表传（`{cmd = 500, act = 1}`），与 flow.json 里动作的 route 同构。

## 11.6 其他模块速览

- **utils**：`utils.sleep(ms)`（没有 robot.sleep！）、`utils.time_ms()`、`utils.random_int(n)` / `utils.rand_range(lo, hi)`、`utils.random_string(len)`、`utils.fnv_hash(s)`；
- **json**：`json.decode(str)` / `json.encode(t)`（超 2^53 整数自动转字符串防精度丢失）；
- **log**：`log.debug/info/warn/error(msg)`，自动带机器人标识；
- **share**（需 Redis）：`share.set/get/incr/claim/queue_push/queue_pop`。**注意返回值约定不同**：share 的 err 是**字符串**不是 table；读取类返回三值 `(value, ok, err)`。未启用 Redis 时每次调用返回 `(false, "shared state 未启用…")` 而不是中断脚本——启动期的 share 检查（第 1 章）才是硬闸。

## 11.7 阻塞语义：谁在等、谁在跑

脚本里的等待（sleep / tcp_request / tcp_listen / share）是**协作式**的：挂起的是**本机器人的主流程**，此时——

- 连接的收包 pump 照跑；**心跳照发**（第 10 章）；
- 到达的推送回调（on_message）会在等待窗口被调度执行；
- 其他机器人的流程完全不受影响。

两条限制：await 类调用**不能**包在 `pcall`/`coroutine.create` 里（fail-loud 报错）；pcall 包纯同步逻辑（get/set/log）是合法的。没有脚本热更、脚本之间不能互相 require——一个脚本一个文件，改了要重启任务。

## 11.8 lua: 条件与 on_message 回调

**条件脚本**（`"condition": "lua:check_ready.lua"`）：同样是 `execute(r)`，但**必须 return 真 boolean**——返回数字/nil 视为错误、条件按 false。本章 `check_ready.lua` 就是范本。

**推送回调**（listen 的 `script`，第 8 章三态之一）：

```lua
function on_message(r, msg)
    -- msg：配了 s2cProto 是解码后的表；没配是二进制 string（脚本按 type(msg) 防御）
    -- 返回值被忽略；失败只记回调指标，不影响主流程
    -- 可以调 await 类 API（比如收到邀请直接 tcp_request 应答）
end
```

## 11.9 范例剖析：仓库自带的两个范本

**`conf/scripts/connect_logic.lua`（建连 + 密钥交换，35 行）**——声明式表达不了的典型：

```lua
local err = network.connect_tcp("logic", logicAddress)   -- 1. 建连
if err ~= nil then log.error(...); return err end
local err, keyBody = network.tcp_request("logic", {cmd=0, act=0})  -- 2. 密钥交换（不传消息体）
if keyBody == "" then return robot.error(12, "密钥交换返回空") end
network.set_tcp_secret_key("logic", keyBody)             -- 3. 装密钥 → 挂起的心跳自动启动
return nil
```

这是仓库自带 flow.json 建连走 Lua 而非 `tcpConnect` 的原因（第 5 章留的伏笔）。

**`conf/scripts/system_shop_buy.lua`（get_view 范本，84 行）**：`robot.get_view("systemShopData")` 借视图 → `proto.list_size/list_get/iter_list` 挑商品 → `proto.create + set_field` 构造购买消息 → `network.tcp_request` 发出。头注释写明了选型理由：整读 ≈ 8ms + 1.2MB 分配，视图微秒/KB 级。

## 11.10 运行与观察

```bash
go run ./cmd/stressbot -config tutorial/examples/ch11/stressbot.toml
```

无服务器时：fetch 失败 → `check_ready.lua` 读不到 `playerLevel` 返回 false → 走 `skipNote` 分支（`SmartBuy` 不执行）。想直接看 `smart_buy.lua` 的防御分支，把 `readyGate` 的条件临时改成 `"state:account != \"\""`（恒真）再跑：`playerData 缺失，跳过购买` 的 warn 会出现，脚本返回 nil——**"上游失败、脚本安静让路"**的推荐写法。有服务器时（【需目标服务器】）完整走通：视图算剩余格子 → 逛店 → 按剩余量购买。

## 11.11 练习与验收

练习：给 `smart_buy.lua` 的购买请求加超时重试一次（err.code == 4 时再发一次），体验 err table 的分支处理。

- [ ] 能背出契约三铁律与"没有 await_listen/robot.sleep/utils.now"的正确名字
- [ ] 能说出 get vs get_view 的选择口诀和两者的报错互斥
- [ ] 知道 tcp_listen 前要先 ensure_tcp_listener
- [ ] 知道挂起时心跳照发、pcall 不能包 await、share 的 err 是字符串

## 延伸阅读

- 实现与内存优化：[docs/lua-runtime.md](../docs/lua-runtime.md)
- 下一章：[第 12 章 加压策略与运行参数](ch12-rampup.md)
