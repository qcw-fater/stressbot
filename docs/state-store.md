# 状态层：单机器人 Store 与 Redis 共享态 -- 技术文档

> **本文对照实际代码编写，反映已实施状态。**
>
> 核对范围（2026-09-20，工作区 `D:\Gitee\stressbot`）：
> `state/`（store.go / overlay.go）、`state/shared/` 全部源码（store.go / redis_store.go / scripts.go / codec.go / config.go / detect.go）、
> `robot/robot.go`（身份键、监听回调存储路径）、`engine/action.go`（store 映射三路径、过滤器、HTTP JSON 存储）、
> `engine/heartbeat.go` 与 `engine/heartbeat_plan.go`（pump 侧 store 访问）、`flow/prepare.go`（clearState 校验）、
> `binding/filter.go`、`script/api_robot.go`（get/get_view/set 转换）、`script/api_share.go`（share 模块 20 函数）、
> `script/statekeys_http.go`（观测端点）、`standalone/app.go` / `agent/task/runner.go` / `admin/app.go` / `admin/httpapi/task_routes.go` / `admin/task/shared_cleanup.go`（Redis 装配）、
> `conf/stressbot.toml` / `conf/admin.toml`（[redis] 段）。
> 文中 `文件:行号` 均指向当前源码。存储形态的内存特性结论引用 [lua-runtime.md](lua-runtime.md) 第 6 节与 [proto-schema.md](proto-schema.md) 第 4 节，本文不重复展开。

---

## 目录

1. [定位与并发模型](#1-定位与并发模型)
2. [存储形态与生命周期](#2-存储形态与生命周期)
3. [路径系统](#3-路径系统)
4. [Overlay 覆盖层](#4-overlay-覆盖层)
5. [比较与过滤](#5-比较与过滤)
6. [Redis 共享态（state/shared）](#6-redis-共享态stateshared)
7. [观测](#7-观测)
8. [附录：API 速查](#8-附录api-速查)

---

## 1. 定位与并发模型

`state.Store` 是单机器人的线程安全 key-value 存储：`map[string]any` + `sync.RWMutex`（`state/store.go:58-61`）。每个 Robot 创建时独占一个实例（`robot/robot.go:136`），存放登录响应、业务中间数据等；压测流程完全数据驱动，无需为每个业务字段预置强类型访问器（`state/store.go:1-3`）。

### 1.1 谁写、谁读

| 参与方 | 访问内容 | 位置 |
| --- | --- | --- |
| 执行器 goroutine（业务流程） | 嵌套容器读写：声明式 store 映射、setState、clearState、Lua `robot.set/set_path/delete/clear/increment` | `engine/action.go:1009-1027`、`engine/action.go:1178-1350`、`script/api_robot.go:163-251` |
| 监听回调（Go-store 形态） | pump 内物化取值，`SetPath` 经 Robot 任务队列投递到执行器执行——pump 绝不直接写嵌套容器 | `robot/robot.go:1079-1109` |
| 监听回调（Lua 脚本形态） | pump 只深拷贝消息快照 + 投递任务队列，`on_message` 由执行器在节点边界串行执行（可经 `robot.*` 读写 store） | `robot/robot.go:1040-1051`、`robot/robot.go:1194-1217` |
| 连接 pump goroutine（心跳） | 仅顶层标量访问：state 源读 `Get` + `ToInt64Safe`/`ToFloat64Safe`，stateCounter 自增 `IncrementInt64` | `engine/heartbeat.go:172-183`、`engine/heartbeat.go:191`、`engine/heartbeat.go:245-256` |
| 连接 pump goroutine（心跳 proto 模式） | `BuildProtoBody` 解析 bindings 时经 `GetList`/`GetMap` 等读顶层容器（读锁内浅拷贝） | `engine/action.go:442-452` |

### 1.2 单写方契约（P1b）

并发契约写在 `state.Store` 的 godoc（`state/store.go:46-57`）：

- **嵌套容器（map/list）的写只发生在执行器 goroutine**——脚本 `robot.set`、声明式 store、Go-store 监听（后者物化后经任务队列投递执行器执行 `SetPath`）。
- **pump 只做顶层标量访问**（心跳 bindings 读与 stateCounter 自增），均在锁内完成、不触碰嵌套容器内部。心跳有硬时序要求，不能改投执行器队列，这是 RWMutex 保留的原因。
- **`GetPath` 命中容器时直接返回内部引用（别名），不再深拷贝**（`state/store.go:573-578`）：调用方（`robot.get_path`、条件求值）全在执行器 goroutine，而嵌套容器唯一写方也是执行器自身——自己读自己写无并发。深拷贝曾是 pump 就地写年代的护栏，单写方化后成为纯 GC churn，已删除。
- `GetList`/`GetMap` 保留**顶层浅拷贝**（`state/store.go:118-123`、`state/store.go:141-144`）：这两个方法同时服务执行器与 pump 两侧读者，执行器替换顶层切片时 pump 持有的旧副本仍自洽；拷贝在读锁内完成，低频调用。

## 2. 存储形态与生命周期

Store 的 value 是 `any`，同一 key 上可能出现五种形态。哪种形态落地取决于**写入路径与 schema 状态**（wire 直达是否可用），与读取方无关：

| 形态 | 产生路径 | 写入点 |
| --- | --- | --- |
| `*protox.WireValue`（wire 字节快照 + 惰性导航） | 请求响应 / 监听推送 / Lua `robot.set` 存响应句柄或视图 | `engine/action.go:1208`、`robot/robot.go:1130-1142`、`script/api_robot.go:477-478`、`script/api_robot.go:481-484` |
| `*protox.Frozen`（解码消息的不可变引用） | schema 影子验证降级时的回落路径；Lua 存裸 proto 消息 | `engine/action.go:1283-1285`、`script/api_robot.go:479-480`、`script/api_robot.go:485-486` |
| `map[string]any` / `[]any` / 标量 | HTTP JSON 响应、setState 绑定取值、Lua 存表（深转换快照） | `engine/action.go:1144-1152`、`engine/action.go:1024`、`script/api_robot.go:494-501` |
| `[]byte`（原始字节） | `s2cProto` 为空时的整存映射（无 schema 可解析，原样存 body） | `engine/action.go:1183-1190` |
| `*state.Overlay`（wire/frozen 基座 + 写覆盖层） | `SetPath` 写嵌套路径时命中 `PathNavigator` 值，包 Overlay 下探 | `state/store.go:279-287`、`state/store.go:309-321` |

要点：

- **请求响应 wire-first**：`parseAndStoreResponse` 不再为存储解码整条消息——结构校验（`protox.ValidateWire`）取代「解码即校验」，整存映射（`Field==""`）存 `*WireValue`，路径映射在 wire 上惰性导航，message 终端产物经保留规划（`PlanWireRetention`）决定复制小 span 还是共享整包快照（`engine/action.go:1165-1209`）。
- **监听推送**：大消息（≥ `protox.DedupMinBytes`）走内容寻址去重（`WireShared`，同一条广播全进程共享同一 `*WireValue`）；小消息独立快照。pump 内只物化待写值（新建容器/不可变引用，所有权跨 goroutine 移交），`SetPath` 投执行器任务队列（`robot/robot.go:1071-1109`）。
- **解码路径**：影子验证失配降级的 schema 走 `parseAndStoreDecoded`——整存 `protox.Freeze(respMsg)`（不可变引用），路径映射 `GetFieldForStore` 按路径取值不展开无关大字段（`engine/action.go:1212-1303`）。
- **JSON 路径**：仅 HTTP 动作且 `contentType` 为 JSON 时使用，`json.Unmarshal` 为 `map[string]any` 后按 store 映射导航（`engine/action.go:1142-1153`）。
- **Lua 写侧形态**（`luaToGoStoreValue`，`script/api_robot.go:457-501`）：响应句柄未改写时零转换复用 body 快照、已改写时重编码为 wire 字节；`WireValue`/`Frozen` 原样存引用；裸 proto 消息 Freeze；函数/协程/channel/未知 userdata 教学报错（静默存 nil 会让脚本把内部故障读成「key 没写进去」）。
- 各形态的内存特性（每机器人独有消息为何只存 wire 字节、广播为何去重、独占解码为何被证伪）见 [lua-runtime.md](lua-runtime.md) 第 6 节与 [proto-schema.md](proto-schema.md) 第 4 节。

消费侧由两个扩展点承接不透明形态：`PathNavigator`（`NavigateSegs` 惰性取路径，`state/store.go:30-32`）与 `ValueMaterializer`（`MaterializeValue` 全量物化纯 Go 树，`state/store.go:40-42`）。`WireValue`/`Frozen`/`Overlay` 同时实现两者；`robot.get` 整读走物化转 Lua 表，`get_view` 借出 wire 惰性视图（`script/api_robot.go:425-451`、`script/api_robot.go:135-161`，完整契约见 [lua-runtime.md](lua-runtime.md) 第 4 节）。

### 2.1 身份键保护

`id` / `index` / `account` 三个键由框架按机器人身份分配，两道防线：

- **stateExtra 不可覆盖**：`NewRobot` 先写 `stateExtra` 再写保留键，冲突键 Warn 后忽略（`robot/robot.go:165-177`，保留键表 `robot/robot.go:301-306`）。若允许覆盖，整批机器人共用同一身份，日志与服务端会话全部对不上。
- **clearState 不可清除**：加载期 `ValidateClearStateKeys` 直接报错（`flow/prepare.go:22-30`，在 `PrepareTaskFlow` 中对每个 clearState 动作检查，`flow/prepare.go:42-46`）；运行期 `execClearState` 再校验一次，命中返回 `ErrStateConfig`（错误码 50，`engine/action.go:996-999`、`errcode/code.go:48`）。

### 2.2 生命周期

Store 与 Robot 同生命周期：`NewRobot` 创建（`robot/robot.go:136`），机器人正常退出时在清理回调里 `state.Clear()`（`robot/robot.go:460`）。**清理超时路径例外**：连接清理阻塞超过 `robotCloseTimeout`（10s）时放弃等待，LState 不归还池、**state 不清空**（避免与卡死的 goroutine 并发访问），泄漏由进程退出时 OS 回收（`robot/robot.go:295-299`）。任务中途清理：`clearState` 动作按键删除（有 2.1 节两道防线）；Lua `robot.clear()` / `robot.delete()` 同口径——身份键 `id`/`index`/`account` 不可清除或删除（Warn 并忽略，`script/api_robot.go` 的 `deleteStateKey` 统一入口，保护键清单与 `flow.IsProtectedStateKey` 同源）。

## 3. 路径系统

### 3.1 语法与分段

路径语法：**点分字段名 + `[N]` 数组下标**，如 `"a.b[0].c"`。`SplitPath` 把它拆为 `["a","b","[0]","c"]`——下标段保留方括号原样成段（`state/store.go:625-656`）；未闭合的 `[` 按普通字符处理。单段导航 `navigateValue`：`[N]` 段要求当前值是 `[]any` 且下标在界内，否则 nil；普通段要求当前值是 `map[string]any`，取键（`state/store.go:582-606`）。

热路径（`SetPath`/`GetPath`/`NavigatePath`）经 `splitPathCached` 复用分段结果：`sync.Map` 缓存 path → `[]string`，路径集合由配置固定、数量有界，缓存值只按只读方式消费（`state/store.go:608-623`）。导出的 `SplitPath` 保持每次新分配，供需要可变结果的外部调用方。

读写示例（设 state 中 `login` 是整存消息，`heroList` 是脚本存的表）：

```text
GetPath("heroList[0].level")     → heroList[0] 的 level 字段（越界/不存在 → nil）
SetPath("bag.gold", 100)         → bag 不存在则先创建 map 再写入（mkdir -p）
SetPath("heroList[5].x", 1)      → heroList 长度不足 6 → 整次跳过（数组不自动创建）
SetPath("login.vip.level", 2)    → login 是 WireValue → 包 Overlay 后写入 vip.level，
                                   login 其余子树仍为 wire 字节（见第 4 节）
```

### 3.2 GetPath / NavigatePath

- `Store.GetPath(path)`：单段等价于 `Get`；空路径或路径不匹配返回 nil。**全程持读锁导航**——`navigateValue` 裸读嵌套 map，不在锁内会与写方并发触发 concurrent map read and map write 崩溃；直接读 `s.data` 而非调 `s.Get`，避免同 goroutine 递归 RLock 在写者等待时死锁（`state/store.go:542-579`）。遇到 `PathNavigator`（如整存的 `WireValue`/`Frozen`）时把剩余路径段整体交给它惰性取值。
- 包级 `NavigatePath(v, path)`：从**任意值**（不限于 Store 顶层 key）按同语法逐段提取，供 engine/robot 包复用（`state/store.go:331-352`）。

### 3.3 SetPath 语义

`Store.SetPath(path, value)`（`state/store.go:252-329`）：

- 单段路径等价于 `Set`；空路径不操作。
- **中间 map 不存在时自动创建**（类似 `mkdir -p`）；已存在但非容器时覆盖为新 map——**`PathNavigator` 例外**：包进 `Overlay` 继续下探（COW），未触碰的兄弟子树保留基座惰性形态，不会被整键清掉（见第 4 节）。
- **数组下标段不自动创建**：`[N]` 要求对应位置已是 `[]any` 且长度足够，越界或类型不符**跳过整次写入**（不报错，`state/store.go:257`、`state/store.go:302-304`、`state/store.go:366-376`）。
- 最后一段为终端赋值；目标位置本身是导航值时被整体替换（写入就是要覆盖它，不物化终端，`state/store.go:326-328`）。

### 3.4 与 filter path 的区别

过滤器用的路径是另一套求值器 `navigatePathValues`（`engine/action.go:891-936`），**只用于 filters**，不影响 binding.path / store.field 的单值语义：

- 支持 `[]` **数组通配**段：把当前层全部列表元素展开进候选集（`engine/action.go:904-908`）——如 `items[].itemId` 取每个元素再下钻；
- 也支持 `[N]` 具体下标（`engine/action.go:915-921`）；
- 逐段「存在才推进」，任一层全军覆没返回 nil（空集，交给 mode 聚合判定，见 5.2）；
- 对 `PathNavigator` 惰性值逐段消费，与 map 分支语义一致（`engine/action.go:922-927`）。

## 4. Overlay 覆盖层

`state.Overlay` 是不可变导航基座（`PathNavigator`，如 `WireValue`/`Frozen`）之上的**可变覆盖层**，`robot.set_path` 对 wire-first 整存值做写时物化（COW）的写模型（`state/overlay.go:1-15`）。

### 4.1 写路径：为何不解码、不重编码

`SetPath` 沿写路径把「基座的子节点」**按需**物化进 `overrides`（`childForWrite`，`state/overlay.go:119-164`）：

- message 子节点继续包 `Overlay`（保持惰性）；repeated/map 终端物化为可变 `[]any`/`map[string]any`；
- 基座导航产物是现场新建容器，覆盖层直接接管所有权（`state/overlay.go:154-157`）；标量基座按旧语义覆盖为新 map 继续下探；
- **未触碰的兄弟子树保持基座字节形态（零展开）**——常驻体积 ∝ 实际写入的路径数，而非消息 schema 大小（`state/overlay.go:8-10`）。

全程不整树解码、不重编码 wire 字节：这是 2026-07 剖面结论的直接应用（每机器人独有消息的解码态表示 ~600KB/机器人，wire 字节是唯一小一个数量级的形态，见 [proto-schema.md](proto-schema.md) 第 4.4 节）。Overlay 只由执行器 goroutine 在 Store 写锁内改写，读经 Store 读锁或执行器自身（`state/overlay.go:12-13`）。

### 4.2 读路径与物化

`Overlay` 实现 `PathNavigator`：读先查 `overrides`，未命中回落基座惰性导航（`state/overlay.go:33-52`）。细节：

- 墓碑（`tombstone`）：`setChild` 写 nil 时登记墓碑——读视为不存在，物化时从基座结果中删键（`state/overlay.go:17-19`、`state/overlay.go:166-176`）。与旧 map 语义 `map[seg]=nil` 保键不同，两者经 `GetPath` 读到的都是 nil。
- 基座是 message 语义节点（map 键空间），数组下标段不匹配、返回未找到（`state/overlay.go:38-41`）。
- `MaterializeValue`：基座全量物化后套用覆盖层，产出纯 `map[string]any` 树（嵌套导航值递归物化，容器复制不改写内部状态，`state/overlay.go:74-114`）——供 `robot.get` 整读（`script/api_robot.go:449-451`）与 `GetMap`（`state/store.go:153-159`）消费。

### 4.3 与视图的互斥

`robot.get_view` 只接受 `*WireValue` 与 `*Frozen` 两种消息形态（`script/api_robot.go:145-153`）；key 上是 Overlay（即被 `set_path` 改写过）时**大声报错指路**，不静默给错形态（`script/api_robot.go:154-158`）：

```
robot.get_view: key %q 的存储形态是 %T, 不是可借阅的消息字节。
整表加工请用 robot.get(key); 被 set_path 改写过(存在写覆盖层)的 key 同样请用 robot.get
```

`robot.get_path` 不受影响：Overlay 是 `PathNavigator`，路径读照常工作。key 被 `robot.set` 整键覆盖后，新值是什么形态就是什么形态——视图是借出时那份不可变字节的快照引用，无失效协议。

## 5. 比较与过滤

### 5.1 CompareValues 全表

`state.CompareValues(a, b, op)`（`state/store.go:673-764`）支持 12 个运算符，其中 6 个比较运算符有符号别名（**空串等价于 `eq`**，`state/store.go:687`）：

| 运算符（别名） | 语义 |
| --- | --- |
| `eq`（`==`、空串） | 双方皆数值 → 数值比较；nil==nil 为真；否则按 `%v` 字符串比较 |
| `neq`（`!=`） | eq 取反（同上分派） |
| `gt`（`>`）/ `gte`（`>=`）/ `lt`（`<`）/ `lte`（`<=`） | **仅数值比较**；任一方非数值直接 false（`state/store.go:711-733`） |
| `contains` / `notContains` | a 是 `[]any` → 逐元素 `DeepEqual`；否则按 `%v` 字符串包含（`state/store.go:767-782`） |
| `in` / `notIn` | b 必须是 `[]any`，a 与任一元素 `DeepEqual`（`state/store.go:741-754`） |
| `notNil` / `isNil` | 只看 a 是否为 nil，b 忽略（`state/store.go:756-760`） |

**数值感知**：先经 `ToFloat64Safe` 判定双方是否数值类型（`state/store.go:676-677`）；**nil 与数值对方比较时按 0 参与数值比较**（`state/store.go:679-684`）——如 state key 缺失时 `gt 0` 判 0>0 为假，而不是类型不匹配直接 false。`DeepEqual`（`state/store.go:785-795`）同样数值感知：双方皆数值按数值相等，否则 `%v` 字符串相等。数值判定只认数字类型（`ToFloat64Safe` 第二返回值，`state/store.go:660-671`），数字字符串 `"1"` 与 `1` 经 `%v` 回落才可能相等。

### 5.2 FilterDef 与 mode 聚合

`binding.FilterDef` 五字段（`binding/filter.go:4-10`）：`path`（候选取值路径，支持 `[]` 通配）、`op`（上表运算符）、`value`（字面比较目标）、`source`（非空时改为从 `store.Get(source)` 取比较目标，`engine/action.go:1371-1376`）、`mode`（多候选聚合）。

`matchFilters` 对列表项逐条过滤：**多个 FilterDef 之间是 AND**（任一不满足即剔除，`engine/action.go:1367-1383`）。单个 FilterDef 内，`navigatePathValues` 取出的多个候选值按 `mode` 聚合（`matchFilterValues`，`engine/action.go:1386-1421`；常量 `binding/constants.go:27-29`）：

| mode | 聚合语义 |
| --- | --- |
| `any`（缺省，未知值同此） | 任一候选满足即真 |
| `all` | 全部候选满足才真 |
| `none` | 无一候选满足才真 |

**空 values（路径没取到任何值）时只有 `none` 为真**（`engine/action.go:1387-1389`）——`any`/`all` 对空集为假。这使 `mode: none` + `path: "items[].itemId"` 可表达「列表里没有匹配项」。

配置示例（筛选商店列表）：

```jsonc
{
  "type": "stateRandom", "field": "shopItemId", "source": "shopData",
  "filters": [
    // 候选取 shopData 各元素的 ID（[] 通配）；比较目标从 state 的 cfgShopId 取
    { "path": "shopData[].ID", "op": "eq", "source": "cfgShopId", "mode": "any" },
    // 每个元素的 stock 字段都 > 0 才保留
    { "path": "shopData[].stock", "op": "gt", "value": 0, "mode": "all" }
  ]
}
```

（FieldBind 的 state key 字段名是 `source`，`binding/field.go:33`；stateRandom 无过滤器时走 `PickFromList` 读锁内零拷贝选取，有过滤器时 `GetList` 取快照再筛，`engine/action.go:654-668`。）

比较语义示例（`CompareValues` 的 nil 数值感知，`state/store.go:679-684`）：

```text
a = nil, b = 0,  op = "eq"  → true   （nil 按数值 0 参与）
a = nil, b = 0,  op = "gt"  → false  （0 > 0）
a = nil, b = nil, op = "eq" → true   （双双非数值：nil==nil）
a = "1", b = 1,   op = "eq" → true   （"1" 不是数值类型，走 %v 字符串比较 "1"=="1"）
a = "1", b = 1,   op = "gt" → false  （gt 仅数值比较，任一方非数值直接 false）
```

### 5.3 值类型转换

Store 边界的公开转换函数（调用方含心跳、条件求值、binding 解析）：

- `ToInt64` / `ToInt64Safe`（`state/store.go:463-513`）：接受全部 Go 数值类型、bool（true→1）、**十进制整数字符串**（`strconv.ParseInt`，TrimSpace）——字符串分支用于承接 Lua 层为避免大整数精度丢失而保存的 int64 文本；`uint64`/`uint` 超 `MaxInt64` 拒绝。Safe 版返回是否成功，非 Safe 失败给 0。
- `ToFloat64` / `ToFloat64Safe`（`state/store.go:516-540`、`state/store.go:660-671`）：数值类型直转；Safe 版第二返回值指示**原始值是否数字类型**（字符串数字不算）。
- 私有 `toInt`（`state/store.go:435-459`）：`GetInt` 的实现，非数值给 0，无字符串分支。

## 6. Redis 共享态（state/shared）

`state/shared` 提供 Redis 支撑的跨 Robot / 跨 Agent 共享状态。设计要点（`state/shared/config.go:1-8`）：只支持 Redis、不做进程内 memory 后端（避免误以为本地共享能跨 Agent 生效）；所有 key 自动加 `keyPrefix:runId:type:userKey` 前缀按任务隔离；写入时维护任务级 key 索引集合供任务结束统一清理；只开放受控共享原语（KV / Counter / Claim / Queue / Hash），不是完整 Redis 客户端。

接口 `shared.Store` 定义五个命名空间共 20 个数据方法 + 生命周期方法（`state/shared/store.go:18-58`）；`RedisStore` 是唯一实现，一个实例对应一个任务运行（runID），同进程所有 Robot 共享、分布式下各 Agent 各持实例但 runID 相同从而落同一命名空间（`state/shared/redis_store.go:29-37`）。

### 6.1 连接与启用判定

- 连接：`NewRedisStore` → `dialRedis` 先按单机客户端 PING，再 `CLUSTER INFO` 探测（`cluster_enabled:1` 即集群）自动切换 `ClusterClient`，零配置适配单机 / 真集群 / proxy 集群（`state/shared/redis_store.go:62-112`）。
- key 布局 `<prefix>:{<runID>}:<type>:<userKey>`：`{runID}` 是 Cluster hashtag，本 run 全部数据 key 与索引集合 key 同 slot，EVAL 多 KEYS 不 CROSSSLOT；runID 含花括号会在创建时 fail-fast（`state/shared/redis_store.go:46-59`、`state/shared/redis_store.go:136-148`）。
- 配置（TOML `[redis]` 段，`state/shared/config.go:31-40`）：`host` 空 = 不启用；`keyPrefix` 默认 `stressbot`、`defaultClaimTTL` 默认 30s、`opTimeout` 默认 2s、拨号 5s / 读写 2s（`state/shared/config.go:19-25`）；连接池参数在 `[redis.pool]` 子表。单机配 `conf/stressbot.toml`（`conf/stressbot.toml:164` 起）、Admin 配 `conf/admin.toml`（`conf/admin.toml:115` 起）；Agent 不本地配置，Redis 参数随任务分配下发。样例：

```toml
[redis]
host = "127.0.0.1"        # 留空 = 不启用共享状态
port = 6379
# username / password 可选
keyPrefix = "stressbot"   # key 前缀（默认 stressbot）
defaultClaimTTL = "30s"   # claim 缺省租约时长
opTimeout = "2s"          # 单次操作超时（Lua 层派生 opCtx 的上限）

# [redis.pool]
# maxOpenConns / maxIdleConns / dialTimeout / readTimeout / writeTimeout / connMaxLifetime
```
- **启用判定按「脚本是否真的用 share」**：`UsesShare` 剥离 Lua 注释后检测 `require("share")`（`state/shared/detect.go:17-29`）。单机模式扫描 scripts 目录，脚本用 share 但未配置 Redis → 启动失败（`standalone/app.go:147-158`）；脚本没用 share 则即使配了 Redis 也不连接（`standalone/app.go:152-155`）。分布式模式 Admin 在任务启动时检测（`admin/httpapi/task_routes.go:395-401`），未配置 Redis 直接拒绝启动任务（`ErrSharedUnavailable`），Redis 配置与 runID 随任务分配下发，Agent 据此建 store（`agent/task/runner.go:173-199`）。

### 6.2 五个命名空间的后端实现与原子性

| 命名空间 | Redis 结构 | 原子性机制 |
| --- | --- | --- |
| KV（`kv`） | String（JSON 编码） | 写入经 Lua 脚本 `setScript`：SET + 可选 EXPIRE + 索引 SADD 原子完成（`state/shared/redis_store.go:179-186`、`state/shared/scripts.go:9-18`） |
| 计数器（`counter`） | String（整数） | `incrScript`：INCRBY + 可选 EXPIRE + SADD，返回新值（`state/shared/redis_store.go:241-244`、`state/shared/scripts.go:21-29`）；delta=0 合法（「+0 读当前值」） |
| 锁（`claim`） | String（owner） | `claimScript` SET NX (+EX)（`state/shared/scripts.go:32-45`）；release/renew 见 6.3 |
| 队列（`queue`） | List（FIFO） | `queuePushScript`：RPUSH + 可选 EXPIRE + SADD（`state/shared/redis_store.go:302-309`）；pop 是单命令 LPOP（`state/shared/redis_store.go:312-326`） |
| Hash（`hash`） | Hash | `hashSetScript` / `hashIncrScript`：HSET/HINCRBY + 可选 EXPIRE + SADD（`state/shared/redis_store.go:345-352`、`state/shared/redis_store.go:407-410`、`state/shared/scripts.go:75-94`） |

通用约定：

- **ttl==0 表示不设置过期**；正数不足 1 秒按 1 秒，避免亚秒截断成 0（`ttlSeconds`，`state/shared/redis_store.go:159-168`）。`Expire`/`QueueExpire`/`HashExpire` 对 ttl<=0 一律 no-op 返回 false——直接把 0 传给 EXPIRE 会**立即删 key**，违反契约（`state/shared/redis_store.go:226-235`、`state/shared/redis_store.go:333-340`）。
- **`Delete`/`Exists`/`Expire` 作用于数据类型全集**（kv+counter+queue+hash 四个命名空间同名 userKey），**不含 claim**——锁有独立 release/renew 语义，不应被通用 del 绕过 owner 校验删除（`state/shared/redis_store.go:25-27`、`state/shared/redis_store.go:151-157`）。跨命名空间 Expire 用 `expireScript` 在服务端一次遍历，把 4 次 RTT 合并为 1 次（`state/shared/redis_store.go:219-235`、`state/shared/scripts.go:96-108`）。
- key 不存在不是错误：读取类返回 `(零值, false, nil)`（如 `Get`，`state/shared/redis_store.go:189-203`）；`HashGetAll` 用一次 EXISTS 区分「不存在」与「空 hash」（`state/shared/redis_store.go:371-399`）。
- **值编码**：JSON 字符串，`decodeValue` 用 `Decoder.UseNumber` 避免数字过早变 float64——`|n| <= 2^53` 的整数还原 int64、超出保留字符串（与 Lua 侧大整数策略一致）、含小数/指数还原 float64（`state/shared/codec.go:13`、`state/shared/codec.go:43-88`）。

### 6.3 租约锁语义

四个方法构成租约式锁（`state/shared/redis_store.go:246-297`）：

- `Claim(key, owner, ttl)`：锁不存在或已超时被清则获取成功并设置租约（`SET NX EX`，owner 为值）；已被他人持有返回 false。**ttl<=0 时用 DefaultClaimTTL**（配置默认 30s）。
- `Release(key, owner)`：owner 匹配才 DEL（Lua 校验，防误删他人锁）；不存在或非本人返回 false。
- `Owner(key)`：查询当前持有者；锁不存在返回 `("", false, nil)`。
- `Renew(key, owner, ttl)`：owner 匹配才把 TTL 重置为新值——**租约到期即失效，持有者必须续租**；非本人或已过期返回 false。

### 6.4 Lua share 模块（20 函数）

`share` 模块把 `shared.Store` 暴露给脚本（注册表 `script/api_share.go:35-66`），与后端接口一一对应（签名摘自各函数 godoc）：

| 分组 | 函数（Lua 签名） |
| --- | --- |
| KV（5） | `set(key, value [, ttlSec]) -> ok, err` · `get(key) -> value, ok, err` · `del(key) -> ok, err` · `exists(key) -> exists, err` · `expire(key, ttlSec) -> ok, err` |
| 计数器（1） | `incr(key [, delta [, ttlSec]]) -> value, err`（delta 缺省 1） |
| 锁（4） | `claim(key, owner [, ttlSec]) -> ok, err` · `release(key, owner) -> released, err` · `owner(key) -> owner, err` · `renew(key, owner [, ttlSec]) -> ok, err` |
| 队列（4） | `queue_push(key, value [, ttlSec]) -> ok, err` · `queue_pop(key) -> value, ok, err`（ok=false 队列空）· `queue_len(key) -> n, err` · `queue_expire(key, ttlSec) -> ok, err` |
| Hash（6） | `hash_set(key, field, value [, ttlSec]) -> ok, err` · `hash_get(key, field) -> value, ok, err` · `hash_get_all(key) -> table, err`（不存在 nil、空 hash `{}`）· `hash_del(key, field) -> ok, err` · `hash_incr(key, field [, delta [, ttlSec]]) -> value, err` · `hash_expire(key, ttlSec) -> ok, err` |

ttlSec 缺省 / 非正数 = 不设置过期（`optTTL`，`script/api_share.go:133-147`）；claim/renew 的 ttlSec 缺省时后端用 `DefaultClaimTTL`（`state/shared/redis_store.go:250-253`、`state/shared/redis_store.go:287-290`）。完整用法与示例见根 README 的 Lua API 参考。共性约定（`script/api_share.go:18-34`）：

- 写入/判定类返回 `(value_or_ok, err)`；读取类（get/hash_get/queue_pop）返回 `(value, ok, err)`，ok=false 表示不存在/空队列，与「存储的 JSON null」区分。
- **全部协作式**（Class B）：每个 Redis 往返经 `awaitIO` 投递后台协程，等待窗口内执行器 goroutine 持续 drain 任务队列（跑 listen 回调），Redis 往返不饿死其他协作式工作；操作 ctx 继承 Robot 生命周期并受 `OpTimeout` 限制（`script/api_share.go:121-131`）。与所有 `await_*` 一样只能在脚本顶层直接调用，不可置于 pcall / 子协程（fail-loud）。
- `del`/`exists`/`expire` 在 Lua 侧同样是「清/查/设该 key 名下所有数据形态（kv+counter+queue+hash）」语义，锁要走 `release`/`renew`（`script/api_share.go:25-27`）。

### 6.5 失败语义与生命周期

- **未启用**：`Context.Shared == nil` 时各函数返回 `(false/nil, "shared state 未启用，请在服务器配置中启用 Redis")`——即 `shared.ErrNotEnabled`（`state/shared/store.go:10`、`script/api_share.go:70-84`），返回错误值而非中断脚本。单机模式下该状态通常到不了脚本：脚本用 share 而未配 Redis 在启动期就失败（`standalone/app.go:156-158`）。
- **网络/执行错误**：以 err 字符串返回给 Lua（`resultVals`/`resultVals3`，`script/api_share.go:105-118`），超时由 `OpTimeout` 派生的 opCtx 截断。
- **清理**：`Cleanup` 用 SSCAN 迭代索引集合、按 500 一批 DEL，最后删索引集合本身（`state/shared/redis_store.go:421-464`）。单机模式任务结束时 Cleanup + Close（`standalone/app.go:174-185`）；分布式模式 **Agent 只 Close、统一 Cleanup 由 Admin 在任务终态触发**——多 Agent 共享同一 runId，某个 Agent 先结束就删 key 会删掉别人还在用的（`agent/task/runner.go:173-175`、`admin/task/shared_cleanup.go:127-136`）。Admin 启动时对 Redis 做一次 PING 验证连通性（runID `admin-ping`，`admin/app.go:200-215`）。

## 7. 观测

`/debug/statekeys` 输出 `robot.get` / `robot.get_path` / `robot.get_view` 的按 key 调用计数，用于定位「高频整读大 key」热点（CPU 剖面只能看到 goValueToLua 总量，看不到 key 名）。挂在进程默认 HTTP mux（init 注册，`script/statekeys_http.go:118-159`），启用 pprof 的进程自动获得（与 `/debug/sched` 同机制）。

- **默认关闭**，`?enable=1` 打开、`?enable=0` 关闭（`script/statekeys_http.go:28-33`、`script/statekeys_http.go:120-126`）：埋点要按 `kind+":"+name` 现拼 map 键（一次堆分配），摊在每次 get 上会吃常态压测的 CPU 和 GC，需要时再开。
- 行格式 `key = kind:name`，三列计数（`script/statekeys_http.go:104-109`）：
  - `calls`：调用次数（kind ∈ `get` / `get_path` / `view`，view 只计 calls——视图零物化，`script/statekeys_http.go:95-102`）；
  - `tables`：返回值需构建 Lua 表（容器转换，成本 ∝ 树大小；`WireValue`/`ValueMaterializer`/普通容器都计，`script/statekeys_http.go:80-92`）；
  - `wireDecodes`：容器来自 wire 形态，转换前还要先解码。
- 判读：calls 高 + tables 高的 key 是整读热点；`wireDecodes ≈ tables` 说明该 key 走 wire-first 留存、直转器收益直接作用于它；迁移某热点 key 到 `get_view` 后，应看到其 `get:` 行的 tables/wireDecodes 掉零、`view:` 行上量。
- key 集合由脚本字面量决定、天然有界，防御性上限 4096 之后归入 `__overflow__`（`script/statekeys_http.go:35`、`script/statekeys_http.go:51-69`）；`?reset=1` 读后清零（分窗口对比）；行按 tables 降序、次按 calls 降序排列。

## 8. 附录：API 速查

`state.Store` 导出方法 20 个（定义行号）：

| 类别 | 方法 |
| --- | --- |
| 基础读写 | `Set`:69 · `Get`:76 · `Delete`:384 · `Clear`:391 · `Has`:427 · `Keys`:416 |
| 类型化读 | `GetInt`:84 · `GetInt32`:92 · `GetInt64`:97 · `GetString`:105 · `GetList`:124 · `GetMap`:145 |
| 容器选取 | `ListLen`:165 · `PickFromList`:180 · `PickMapKey`:197 · `PickMapValue`:226 |
| 路径 | `SetPath`:259 · `GetPath`:545 |

（均位于 `state/store.go`。）

包级函数：`NavigatePath`（`state/store.go:335`）、`SplitPath`（`state/store.go:626`）、`CompareValues`（`state/store.go:675`）、`DeepEqual`（`state/store.go:785`）、`ToInt64`（`state/store.go:463`）、`ToInt64Safe`（`state/store.go:472`）、`ToFloat64`（`state/store.go:516`）、`ToFloat64Safe`（`state/store.go:660`）。

`state.Overlay`：`NewOverlay`（`state/overlay.go:28`）、`NavigateSegs`（`state/overlay.go:33`）、`MaterializeValue`（`state/overlay.go:74`）；写侧 `childForWrite`/`setChild` 为 Store 锁内专用。

扩展接口：`PathNavigator`（`state/store.go:30`）、`ValueMaterializer`（`state/store.go:40`）。

`state/shared`：接口 `Store`（`state/shared/store.go:18`）、实现 `RedisStore`（`state/shared/redis_store.go:33`）、错误 `ErrNotEnabled`（`state/shared/store.go:10`）、配置 `RedisConfig.Resolve`（`state/shared/config.go:67`）、脚本使用检测 `UsesShare`（`state/shared/detect.go:17`）。
