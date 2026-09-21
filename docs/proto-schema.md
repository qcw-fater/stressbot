# 动态 proto 加载与 protox wire 数据结构 -- 技术文档

> 本文对照实际代码编写，反映**已实施状态**。核对日期 2026-09-20。
>
> 核对范围：`protocol/protox/` 全部非测试源文件（loader / registry / factory / wirevalue / wireview / wirewalk / wirenav / frozen / dedup / dedup_frozen / dedup_http / wireshadow）与测试文件、`runner/resources.go`、`engine/action.go`（响应存储路径）、`robot/robot.go`（监听回调路径）、`script/`（wire_lua / api_network / api_proto / api_robot / runtime / statekeys_http 消费点）、`standalone/config.go`、`cmd/stressbot/main.go`、`agent/task/runner.go`、`protocol/codec_resolver.go`、`internal/lru/content_lru.go`，以及 `conf/proto/`（当前 116 个 `.proto`）、`conf/flow/flow.json`、`conf/adapter/` 实物。所有 `文件:行号` 引用以当日代码为准。
>
> **与 [lua-runtime.md](lua-runtime.md) 的分工**：本文讲 protox 包本身——schema 加载、三套访问语义、wire 数据结构族与缓存、正确性防线；Lua 侧消费 API（`robot.get` / `robot.get_view` / `proto.*` / `await_listen` 返回值形态、教学报错文案）见该文档第 3~5 节。两文在「wire-first 消费路径」上互相交叉引用。

---

## 目录

1. [总览](#1-总览)
2. [动态 proto 加载](#2-动态-proto-加载)
3. [schema 访问语义：三套读 API](#3-schema-访问语义三套读-api)
4. [wire 数据结构族](#4-wire-数据结构族)
5. [WireCache：广播留存的内容寻址去重](#5-wirecache广播留存的内容寻址去重)
6. [FrozenCache 与 schema 降级回落](#6-frozencache-与-schema-降级回落)
7. [WalkWire 单遍直转器](#7-walkwire-单遍直转器)
8. [导航路径驻留与列表下标早退](#8-导航路径驻留与列表下标早退)
9. [正确性防线与观测端点](#9-正确性防线与观测端点)

---

## 1. 总览

压测任意游戏服务器意味着消息 schema 不可预知：stressbot 在启动时用 `protocompile` 把 `conf/proto/`（当前 116 个文件）动态编译为 descriptor，用 `dynamicpb` 构造消息，全程不生成 Go 代码（`protocol/protox/loader.go:1-3`）。`Factory.Create` 按 `flow.json` 里 `c2sProto` 写的全名（如 `Game.LoginPlayerC2S`，`conf/flow/flow.json:779-780`）创建 C2S 请求；S2C 响应则不再解码常驻——这是 2026-07 wire-first 改造的核心：

- **留存侧**：state 整存映射存 `*WireValue`（原始 wire 字节 + descriptor 的不可变封装，`protocol/protox/wirevalue.go:41-44`），取代解码态 `*Frozen`；
- **消费侧**：整读经 `WalkWire` 单遍直转（零 dynamicpb 中间树），窄读经 wire 惰性视图按需扫描；
- **广播共享**：同一条推送的字节经 `WireCache` 内容寻址塌缩为单份；
- **正确性**：离线差分 fuzz + 线上影子采样双层守护，失配自动把该 schema 降级回解码路径（fail-safe）。

内存结论的依据（AGENTS.md「内存优化约束与已证实结论」，代码注释同步记载）：每机器人独有的大消息（`LoginPlayerDataS2C` ~74KB wire，`protocol/protox/wirevalue.go:22-23`）任何**解码态**表示（dynamicpb / Go map / Lua table）都 ~600KB/机器人；唯一小一个数量级的形态是 wire 字节本身（约 1/5~1/10）。反例同样被剖面钉死：曾把 `playerData` 改为存 `protox.Frozen` 解码引用，同相位 live +217MB @5000 人，已还原（AGENTS.md；`docs/superpowers/plans/2026-07-29-wire-first-state-plane.md` 为设计文档，`protocol/protox/wirevalue.go:21` 引用）。

---

## 2. 动态 proto 加载

### 2.1 Loader：扫描与编译

`Loader` 持有目录列表与可选文件清单（`protocol/protox/loader.go:23-26`）：

- 未指定文件时 `collectFiles` 递归扫描各目录下全部 `.proto`，转为正斜杠相对路径并按名去重（`protocol/protox/loader.go:79-121`）；
- 编译走 `protocompile.Compiler`，Resolver 先从源目录解析 import，再回退标准 proto 导入（`WithStandardImports`，`protocol/protox/loader.go:51-59`）；
- 产物逐个 `RegisterFile` 进新建的 `protoregistry.Files`（`protocol/protox/loader.go:61-72`），空目录（找不到任何 .proto）与编译失败均返回中文 error，fail loud。

### 2.2 Registry：全名 / 短名索引

`Registry` 从 `protoregistry.Files` 构建一张 `fullName → MessageDescriptor` map，构建后只读、无锁（`protocol/protox/registry.go:16-29`）。索引时**同时注册短名**（不含包名的最后一段），仅在该短名无冲突时生效（`protocol/protox/registry.go:41-62`）。`Lookup(name)` 因此接受全名（`example.RequestC2S`）或短名（`PlayerLoginC2S`，`protocol/protox/registry.go:64-69`）。嵌套消息递归索引。

### 2.3 Factory：动态消息工厂与任务生命周期

`Factory` 是单次任务持有的消息工厂（`protocol/protox/factory.go:17-31`）：

- `pathCache sync.Map`：字段路径 → 分段结果缓存（路径集合由配置固定，写一次读多次）；
- `wireCache *WireCache` / `frozenCache *FrozenCache`：两个去重缓存，随 `NewFactory` 以默认上界创建（`protocol/protox/factory.go:34-40`）；
- `Create(name)` 经 `Lookup` 后 `dynamicpb.NewMessage(md)`（`protocol/protox/factory.go:79-85`）；`MessageDescriptor(name)` 供存储点构造 WireValue（`protocol/protox/factory.go:59-62`）；
- 写侧 `SetField` / `SetFieldsFromMap` 支持点分路径与数组下标（`heroData.heroList[0].heroId`，`protocol/protox/factory.go:87-131`）；读侧见第 3 节。

`Close` 必须在任务结束时调用：清空两个缓存、从 `/debug/dedup` 统计列表反注册、清空导航驻留表（`protocol/protox/factory.go:42-57`）——统计列表是包级全局，不反注册会把缓存连同全部条目跨任务钉住，每轮任务累积泄漏数百 MB。

### 2.4 装配链：与 codec、flow.json 的关系

单机与 Agent 共用 `runner.LoadResources`（`runner/resources.go:26-47`）：

```
conf/adapter/*_codec.json  → InferCodecMap → LoadCodecResolver   （协议头编解码，纯 Go）
conf/proto/*.proto         → protox.Loader.Load → Registry → NewFactory（消息体 schema）
conf/flow/flow.json        → 加载校验（validation.ValidateFlow）
```

两者的分工（详见 [adapter-layer.md](adapter-layer.md)）：

- **codec 配置管协议头**。`<proto>_<service>_codec.json` 文件名按首个 `_` 拆为 `<proto>:<service>` 连接串（如 `tcp_logic_codec.json` → `tcp:logic`，`protocol/codec_resolver.go:40-85`）——这里的 `<proto>` 指**传输协议**（tcp/udp），与 protobuf schema 是两个命名空间；
- **protox 管消息体**。`ActionDef.C2SProto` / `S2CProto`（`flow/flow.go:179-180`）与 `ListenDef.S2CProto`（`flow/flow.go:192`）写消息全名或短名，C2S 经 `engine.BuildProtoBody` 构造（`engine/action.go:442-462`），S2C 经 `Registry.Lookup` 解析后走 wire 留存（第 4 节）。`S2CProto` 为空时响应按原始字节直存（`engine/action.go:1183-1189`）。

### 2.5 单机 CLI flag 与 Agent 装配

单机模式 `-proto` flag 覆盖 proto 目录，空值回退 `<config 所在目录>/proto`（`cmd/stressbot/main.go:47`，解析于 `standalone/config.go:105-116` 的 `ResolvePaths`；flow / scripts / adapter 同理）。Agent 从任务 bundle 解包后取 `confDir/proto`（`agent/task/runner.go:100-107`）。

### 2.6 前端独立解析（protobufjs）

前端 FlowEditor 用 protobufjs **独立**解析同一批 `.proto`（与后端 protox 无任何共享）：加载链为本地存储（用户上传）→ 服务器基线 API → `import.meta.glob` 编译期内联兜底（`cmd/web/src/components/FlowEditor/proto/ProtoLoader.ts:1-29`），解析产物投影为轻量 `ProtoRegistry` 索引（`lookupMessage` / `resolveField` 等，`cmd/web/src/components/FlowEditor/proto/ProtoRegistry.ts:1-16`）。同一文件两套解析器是显式取舍：前端要离线可用、后端要在 Go 运行时构造消息。

---

## 3. schema 访问语义：三套读 API

protox 有三套字段读 API，语义差异是**历史事实**而非疏漏，wire 路径逐一保真（`protocol/protox/wireview.go:19-33` 的契约表）：

| API | 语义 | 缺席的单数 message | 错误形态 | wire 对应 |
| --- | --- | --- | --- | --- |
| `Factory.GetField` / `GetListLen` / `GetListItem` | 脚本 `proto.*` API 的语义 | **按默认值实例继续下钻** | 字段不存在 / 越界 / 类型不匹配 = **error** | `GetFieldCompat` 等 Compat 层 |
| `Factory.GetFieldForStore` | `navigatePath(GetFieldMap(msg), path)` 存储语义 | 未设置 → `(nil, false)` not-found | 无（bool） | `WireValue.NavigateSegs` |
| `Factory.GetFieldMap` / `messageToMap` | 整树展开 | 跳过（不进树） | 无 | `WalkWire` 直转 |

**「缺席 message 按默认值实例下钻」**是 `GetField` 的历史语义：`getNestedFieldValue` 对单数 message 字段**不做 Has 检查**，直接 `ref.Get(field).Message()` 继续下钻（`protocol/protox/factory.go:545-547`）——dynamicpb 对未设置 message 的 `Get` 返回默认空实例，于是 `proto.get_path(resp,"record.address")` 在 `record` 缺席时返回 `""` 而非 nil。wire 版 `wireGetNested` 逐字复刻：缺席时以空字节继续扫描（空字节 ≡ 默认值实例，`protocol/protox/wireview.go:120-127`）。这与 `NavigateSegs` 不同轨——后者复刻 `messageToMap` 跳过规则，未设置 message 视为不存在（`protocol/protox/wirevalue.go:251-256`）。

其余两个关键差异（`protocol/protox/wireview.go:24-30`）：

- `GetField` 终端 message 全量物化为 map 树（`wireview.go` 的 `wireToMapTree` 走直转，`protocol/protox/wireview.go:212-219`）；`NavigateSegs` 终端 message 产出子 `*WireValue` 惰性引用（`protocol/protox/wirevalue.go:150-156`）。
- 未知字段 / 非法下标（`[abc]`）/ 越界下标 / 非法嵌套在 `GetField` 语义下是 error（脚本侧 RaiseError），在 `NavigateSegs` 语义下是 not-found。

**messageToMap 的展开规则**（`protocol/protox/factory.go:612-652`）：遍历全部字段描述符（不限已设置），保留 proto3 标量/枚举默认值（`int64=0`、`bool=false`、`string=""`、枚举=0——否则会长职位 `position=0` 会在嵌套层丢失），仅跳过未设置的 message 与空 repeated/map；`Has` 只对支持 presence 的字段调用，标量/枚举一律 `Get` 取默认值。`GetFieldForStore` 与之逐点对齐但**不展开无关大字段**（`protocol/protox/factory.go:250-297`）。

**列表读**：`GetListLen` 不展开内容；`GetListItem` 的 message 元素保留 `proto.Message`（`protocol/protox/factory.go:553-610`）。wire 版即 `ListLenCompat` / `ListItemCompat` / `ListCursorCompat`（`protocol/protox/wireview.go:301-359`），message 元素改为子视图 `*WireValue`。

同一份数据、两条路径的行为对照（`record` 为缺席的单数 message，其内 `address` 为 string）：

| 访问 | `GetField` 语义（proto.get_path） | `NavigateSegs` 语义（store 路径取值） |
| --- | --- | --- |
| `record.address` | `""`（默认值实例下钻，`protocol/protox/factory.go:545-547`） | not-found（messageToMap 跳过规则） |
| `record`（终端） | 整树物化 map（含标量默认值，`protocol/protox/wireview.go:177-183`） | 子 `*WireValue`（惰性引用） |
| 字段名不存在 | error「未找到字段」（`protocol/protox/factory.go:510-513`） | `(nil, false)` |
| `items[99]` 越界 | error「数组索引越界」（`protocol/protox/factory.go:529-532`） | `(nil, false)` |

**与 state 层的接口契约**：`WireValue` 与 `Frozen` 都实现 state 的 `PathNavigator`（`NavigateSegs`）与 `ValueMaterializer`（`MaterializeValue`）两个小接口（`state/store.go:30`、`:40`；`protocol/protox/wirevalue.go:97-101`、`:150-159`；`protocol/protox/frozen.go:46-53`、`:55-71`）——state 层按接口惰性取值/边界物化，不感知具体存储形态；`Overlay` 写覆盖层是第三种实现。这是「存储形态可替换（Frozen → WireValue）而 state 层零改动」的结构基础。

---

## 4. wire 数据结构族

### 4.1 WireValue：字节 + 描述符

```go
type WireValue struct {
    desc protoreflect.MessageDescriptor
    raw  []byte   // 不可变：构造后绝不改写
}
```

（`protocol/protox/wirevalue.go:41-44`）

- **不可变契约**：`raw` 必须是调用方交出所有权的独立快照——网络缓冲区会被 pump 复用，构造点必须先 `WireSnapshot`（`protocol/protox/wirevalue.go:39-58`）。构造后可被多 goroutine 无锁并发读。
- **构造点统一先过 `ValidateWire`**：零分配纯索引游走，校验 tag/长度结构、已知 message 递归、packed 载荷、proto3 string UTF-8（`protocol/protox/wirevalue.go:677-741`）；「校验通过 ≡ proto.Unmarshal 必成功」由差分 fuzz 保证（第 9 节）。
- **导航**：`NavigateSegs` 在字节上按路径段惰性取值（`protocol/protox/wirevalue.go:159-169`），产出表示——终端 message 为子 `*WireValue`（单段零拷贝共享父缓冲，多段拼接，`protocol/protox/wirevalue.go:283-298`）；string/bytes 终端一律复制防钉扎。完整合并语义契约（last-wins / message 段拼接 / oneof 竞争 / packed 混排 / map 同 key 替换）见 `protocol/protox/wirevalue.go:27-37`。
- **全量物化**：`MaterializeValue` 默认走直转器、失败回落解码、再失败上报后返回空 map——「绝不静默」（`protocol/protox/wirevalue.go:97-122`）。`Message()` 现场新建独占解码消息、不缓存（`protocol/protox/wirevalue.go:84-95`）。
- **谁持有字节**：`WireValue` 本身只是引用——字节归其构造来源所有（独立快照、缓存条目或父快照的 span）。释放随引用计数（GC），无失效协议；`CopyDetached` 把子 span 复制为独立小缓冲以解除对父快照的钉扎（`protocol/protox/wirevalue.go:124-131`）。

### 4.2 WireShared：广播共享字节

`Factory.WireShared(name, data)` 相同 `(name, data)` 跨调用方返回**同一个**不可变 `*WireValue`（`protocol/protox/dedup.go:139-144`）——同一场战斗 60 个接收方共享同一份字节快照，协程挂起钉住的也只是这份共享快照而非各自解码树（`script/api_network.go:798-803` 注释）。实现见第 5 节。

### 4.3 Frozen：解码态引用（现为降级形态）

`Frozen` 是解码后 proto 消息的不可变引用封装（`protocol/protox/frozen.go:8-28`）：类型系统挡住 setter（`SetField` 签名不接受 `*Frozen`），可无锁并发读。它曾是 state 整存的主形态（P1a），wire-first 后退居**降级回落路径**——影子失配的 schema 在存储点走 `parseAndStoreDecoded` → `storeResponseProto`，整存仍 `Freeze(respMsg)`（`engine/action.go:1212-1221`、`engine/action.go:1277-1283`）；监听 Go-store 降级分支同理（`robot/robot.go:1169-1189`）。消费侧经 `ParseFrozenShared`（第 6 节）。

### 4.4 剖面结论与「独占解码」红线

| 表示 | 每机器人独有大消息体积 | 出处 |
| --- | --- | --- |
| dynamicpb / Go map / Lua table（解码态） | ~600KB | AGENTS.md；`protocol/protox/wirevalue.go:21-25` |
| wire 字节 | ~1/5~1/10（LoginPlayerDataS2C ~74KB） | 同上 |

由此得出两条被线上剖面证实的硬结论（AGENTS.md，勿重复尝试）：

1. **每机器人独有大消息存解码态引用（Frozen）是净退化**：+217MB @5000 人，已还原；
2. **「脚本消费改独占瞬态解码」被证伪**（029→031 剖面）：60 接收方逐机器人解码使 churn 放大 60 倍（区间 ~1.3TB dynamicpb 分配），协程挂起钉解码树 live 单调 +1.15GB。**wire 惰性视图不触犯此结论**——共享的是 `WireShared` 字节，无解码树可钉。

### 4.5 PlanWireRetention：保留规划

路径映射（`Field != ""`）的导航产物是子 span（引用整包快照）。写入 state 前批量决策（`engine/action.go:1249`、`robot/robot.go:1162`）：

- 同响应还有整存映射（快照必然常驻）→ 子 span 共享快照，零复制；
- 否则比较 Σ(子 span 字节) 与整包字节：复制更省则逐个 `CopyDetached`（快照随导航结束被 GC），共享更省则共享（`protocol/protox/wirevalue.go:850-871`）。

---

## 5. WireCache：广播留存的内容寻址去重

**动机**：同一条服务端广播（商城配置、对局广播）推给多个机器人，逐机器人各留一份字节快照是重复留存。按 `(protoName, 消息字节)` 内容寻址：命中返回同一个 `*WireValue`，留存塌缩为单份、结构校验也只做一次（`protocol/protox/dedup.go:11-15`）。

**wire-first 改造（M3）后的形态**（`protocol/protox/dedup.go:17-25`）：

- 条目从解码态 `*Frozen` 换成 `*WireValue`（原始字节本体）——条目占用就是字节数本身，**容量按原始字节硬上界即真实钉住量**（旧解码态缓存按解码体积估算设界本质是给 ~50 倍失真打补丁）；
- miss 时不再解码整树，只做零分配结构校验 `ValidateWire`。

**参数**（`protocol/protox/dedup.go:35-45`）：

| 参数 | 值 | 说明 |
| --- | --- | --- |
| `DedupMinBytes` | 1024 | 参与去重的消息体下限；小消息不值得哈希+快照 |
| `dedupMaxEntries` | 4096 | 条数上界（线上 5000 人实测 256 条会被高频推送换血） |
| `dedupMaxBytes` | 64MB | 原始字节硬上界 = 真实钉住量 |

任一超限即从 LRU 尾部驱逐（`internal/lru` 的 `ContentLRU`）。驱逐只影响后续命中率，不影响已持引用的机器人。

**碰撞防御**：哈希（进程随机种子的 maphash）只做桶索引，命中判定必须 `protoName` 相等 + 全量 `bytes.Equal`；identity key = `protoName + \x00 分隔 + data`（`protocol/protox/dedup.go:27-29`、`:57-68`）。key 为值类型结构体栈上传递，lookup 零堆分配。

**并发与 double-check**：`wireShared` 先锁内 lookup，miss 后在**锁外**做校验+快照（不阻塞其他 pump 的命中路径），再 `Insert`——相同 key 已存在则跳过并返回已有值（`protocol/protox/dedup.go:82-109`；double-check 语义在 `internal/lru/content_lru.go:71-76`）。

**防污染纪律**：动作响应（请求-响应）逐机器人唯一，**禁入任何去重缓存**——必然 miss 且把真正复用的广播条目从 LRU 挤出去（018→019 实测教训，`protocol/protox/dedup.go:31-33`、`engine/action.go:1175-1177`）。接入点只有两处：监听 Go-store 整存（`robot/robot.go:1131-1136`，≥ `DedupMinBytes`）与 `await_listen` 大消息视图（`script/api_network.go:805-816`）。

> 注意区分：「二见登记」（首见只记哈希标记、二见才缓存）是 `protocol/codec/inflate_cache.go:7-10` 解压去重缓存的机制，WireCache **没有**也不需要——它的防污染靠接入点纪律 + 身份键，不留任何独占内容进表。

**生命周期**：挂 `Factory`（任务级），`Close` 时 purge 并反注册（`protocol/protox/factory.go:47-57`）。观测 `/debug/dedup`（第 9.3 节）。

---

## 6. FrozenCache 与 schema 降级回落

**定位**：内容寻址的共享**解码**缓存（`*Frozen`），与 WireCache（服务留存）互补，服务「脚本消费需要解码态」的场景（`protocol/protox/dedup_frozen.go:10-21`）。它存在的根本原因是 029→031 剖面证伪了独占瞬态解码（第 4.4 节）：消费回落必须落在**共享**解码上。

**参数**：条数 4096 / 体积 256MB（`protocol/protox/dedup_frozen.go:34-40`）。体积按 `estimateDecodedCost` 估算累计——本缓存钉住的是解码树，map/repeated 重度消息的 dynamicpb 树可达原始字节 ~50 倍，按原始字节设界会失真钉住 GB 级（`protocol/protox/dedup_frozen.go:26-29`、`:105-157`）。这与 WireCache 按原始字节设界不矛盾：**两个缓存钉的东西不同**。

**流程**：`ParseFrozenShared(name, data)`——lookup 命中直接返回；miss 锁外 `Parse` 解码、`Freeze` 后 Insert（double-check 以先登记者为准，`protocol/protox/dedup_frozen.go:54-76`、`:162-164`）。

**降级触发与回落路径**：影子验证失配把 schema 全名记入 `degradedSchemas` 并打 error 日志（`protocol/protox/wireshadow.go:143-160`）；此后：

| 回落点 | 位置 |
| --- | --- |
| 存储点改走解码路径（整存 Freeze + 路径 GetFieldForStore） | `engine/action.go:1196-1198`、`robot/robot.go:1126-1128` |
| listen 脚本回落：大消息 `ParseFrozenShared`，小消息独占 `Parse` | `robot/robot.go:1248-1262` |
| `await_listen` 大消息回落共享解码（`wrapFrozenMessage`） | `script/api_network.go:817-824` |
| 响应句柄 `robot.set` 存 Frozen 而非 WireValue | `script/api_robot.go:509-510` |

已存的 WireValue 继续可读——每次读都有 oracle 兜底（`protocol/protox/wireshadow.go:29-34`）。

**常态为空、近零成本**：消费侧首选 wire 直转 / 视图（`robot/robot.go:1204-1216`、`script/api_network.go:794-816`），FrozenCache 只有降级 schema 到达时才有条目。

**退役计划（AGENTS.md 2026-07-29 记，用户已确认方向，尚未实施）**：wire 消费路径经**数轮生产规模压测**且 `/debug/wire` 持续零失配、影子验证按计划稳态关闭后，整体退役「schema 降级回落」——删除 `FrozenCache`、`Frozen` 消费路径与各访问器的解码分支，失配语义从 fail-safe 改为 **fail-stop**（直接报错停测）。这是信心问题而非技术问题；退役前不要提前删——回落必须落在共享解码上（另见 [lua-runtime.md](lua-runtime.md) §5.6）。

---

## 7. WalkWire 单遍直转器

**契约**：在 wire 字节上单遍收集全部字段的最终取值状态，再按 descriptor 字段序经 `WireTreeSink` 回调产出——把「整读」从 `dynamicpb 全量解码 → 遍历解码树建表` 压成 `字节单遍扫描 → 直接建表`，dynamicpb 中间树（每次整读一棵、纯过路分配）整个消失。产出语义与 `messageToMap(dynamicpb.Unmarshal(raw))` 逐字一致（`protocol/protox/wirewalk.go:12-27`）。

**结构**（`protocol/protox/wirewalk.go:113-271`）：

- `Walk(sink)` 入口；`walkWireLevel` 单层级两阶段——`scanLevel` 收集（复用 `wirevalue.go` 的同一批底层原语：last-wins / 段拼接 / oneof 竞争 / packed 混排 / map 同 key 替换），随后按字段序产出（空 map / 空 repeated / 未设置 message 跳过，标量恒产出默认值）；
- oneof 成员切换时清掉前一胜出成员的累计，复刻解码器竞争（`protocol/protox/wirewalk.go:180-189`）；
- group 字段不支持，出现即报错回落（proto3 无 group，`protocol/protox/wirewalk.go:193-195`、`:259-260`）；
- 递归上限与 `ValidateWire` 同源（`protowire.DefaultRecursionLimit`，`protocol/protox/wirewalk.go:108-109`）。

**scratch 池化契约（P2）**：字段累计切片 `[]wireFieldAcc` 走 `sync.Pool`（`protocol/protox/wirewalk.go:82-106`）——整树直转每个 message 层级一次分配，heroList 级整读一次递归几百层，是转换侧分配主源之一。池为瞬态 scratch（GC 周期自动清空、常驻 ∝ 并发转换数），**不属于「内存换 CPU」的缓存交易**。硬纪律：

- **归还前必须逐元素 clear**——`spans`/`elems`/`entries` 引用共享 wire 快照字节，只截断 len 不清元素会让池子经残留引用钉住大缓冲（029→031 剖面证伪过的钉扎形态，`protocol/protox/wirewalk.go:66-80` 的 `reset`）；静止不变量：池内切片 `[0:cap)` 全部处于 reset 态（`protocol/protox/wirewalk.go:86-88`）。守护测试 `TestWalkWireAccsPoolNoContamination`（`protocol/protox/wirewalk_test.go:90`）。
- **产物形态不可池化**（Lua 表 / Go map 归调用方所有，这条的解法是第 4.2 节的视图而非池）。

**两个出口**：Go map 出口 `mapTreeSink`（`protocol/protox/wirewalk.go:275-338`，供 `MaterializeValue`）；Lua 表出口 `luaTreeSink`（`script/wire_lua.go:36-117`，供 `robot.get` 整读——表容量预分配与装箱规则同旧路径逐字一致，`script/wire_lua.go:22-34`）。

---

## 8. 导航路径驻留与列表下标早退

### 8.1 wirenav：一次查表服务两件事（P6）

8000 人剖面上，wire 读路径（`NavigateSegs` / `GetFieldCompat` / `MaterializeAllowed`）每次调用要拼影子采样 key 字符串 + 两次 `sync.Map.LoadOrStore`（实参逃逸）——热路径白付 4 次堆分配；字段解析还要每层 `Fields().ByName`（`protocol/protox/wirenav.go:3-13`）。

`navInfoTable` 把 `(schema, 路径)` 驻留为一个条目 `*navPathInfo`（`protocol/protox/wirenav.go:41-49`）：稳态读 = 1 次哈希 + 1 次 COW map 查找 + 原子计数，零分配零拼接；条目同时携带按 `wireNavigate` 层级推进规则预解析的 fd 链（`compileNavFds`，`protocol/protox/wirenav.go:153-195`），运行时免逐层 ByName。

- 容量上限 8192（配置固定的路径集合远小于此；超限的只能是动态构造路径，不驻留仅回退旧成本，`protocol/protox/wirenav.go:37-39`）；
- COW 写、无锁读；**条目按 descriptor 身份（接口指针）校验**——proto 重载后新描述符替换旧条目并重新首 K 全查，顺带解除对已关闭 Factory 描述符树的钉扎；真哈希碰撞则放弃驻留（`protocol/protox/wirenav.go:104-151`）；
- `navResolve` 为统一入口，返回 `(fd 链, 是否影子校验)`（`protocol/protox/wirenav.go:205-226`）；`Factory.Close` 经 `navResetAll` 清表（`protocol/protox/wirenav.go:228-234`、`protocol/protox/factory.go:54-55`）。

### 8.2 列表下标早退（P6）与不可早退例外

`wireCollectList(b, fd, limit)`：`limit > 0` 时收集到至少 limit 个元素即提前终止扫描（packed 块整块解出可能略多于 limit）——repeated 元素只追加不覆盖，**前缀即定值**，早退不改变取值语义；层级结构在 WireValue 构造点已全量校验，跳过尾部不损失防线（`protocol/protox/wirevalue.go:431-435`）。调用点：`NavigateSegs` 带下标访问（`protocol/protox/wirevalue.go:233-239`）、`wireGetNested`（`protocol/protox/wireview.go:99-100`）、`ListItemCompat`（`protocol/protox/wireview.go:343-344`）。

**不可早退的例外是 protobuf 合并语义的硬约束**（`protocol/protox/wirevalue.go:30-35`，体现在 `wireCollectSingular` / `wireCollectMap` 无 limit 参数）：

- 单数标量：**last-wins**——必须扫完才知道最后一次出现；
- 单数 message：多段**按序拼接**——尾部段影响最终值；
- map：同 key 后一条 entry **整体替换**——尾部可能覆盖前缀。

终端整列表读（`robot.get_path("...heroList")`）与游标遍历天然需要全量，无早退空间。

### 8.3 WireListCursor：游标遍历 O(n²) → O(n)

脚本顺序遍历列表若逐下标 `list_get`，每次都要重扫父层级定位全部元素，整链 O(n²)。`WireListCursor` 一遍收集全部元素跨度（message 元素只存字节跨度引用，零解码零复制），`Item(i)` 按需产出子视图（`protocol/protox/wireview.go:263-296`）。跨度引用父快照字节、`WireValue` 不可变——游标跨协程挂起、跨 key 覆盖均安全，无失效协议。非 repeated 字段返回空游标（对齐 `iter_list` 历史行为，`protocol/protox/wireview.go:298-317`）。

---

## 9. 正确性防线与观测端点

### 9.1 L1：离线差分（wirediff / wirewalk / wireview 测试）

正确性契约：对任意合法 wire 字节 b 与任意路径 p，wire 扫描产物（物化后）≡ 官方解码器产物（`protocol/protox/wirediff_test.go:21-33`）。两条线：

- **确定性矩阵**：protobuf 合并语义每一行为一个用例——重复单数 message 合并 / oneof 后者清前者 / 标量 last-wins / packed 混排 / map 重复 key 替换 / 错误 wire type 按未知字段忽略 / UTF-8 与结构非法拒绝（`TestWireSemantics*`，`protocol/protox/wirediff_test.go:187` 起）；
- **随机差分 fuzz**：随机消息 + wire 级变异，全路径语料双侧比对；另对随机损坏字节做「`ValidateWire` 与 `proto.Unmarshal` 的接受/拒绝完全一致」比对（`TestWireDifferentialFuzz`）。

配套：直转器矩阵与池无污染（`protocol/protox/wirewalk_test.go:38`、`:90`、`:112`、`:137`）、视图 Compat 语义 + 差分 fuzz + 游标语义（`protocol/protox/wireview_test.go:29`、`:100`、`:212`、`:255`）、保留规划（`protocol/protox/wireretention_test.go:28-101`）、两个去重缓存（`protocol/protox/dedup_test.go:28-179`、`dedup_frozen_test.go:13-89`）。

### 9.2 L2/L3：线上影子采样与 schema 降级（/debug/wire）

- **L2 首访全查**：每个 `(schema, 路径)` 前 `shadowFirstK = 3` 次用「dynamicpb 解码 + Frozen 导航」做 oracle 双读比对；**L3 稳态采样**：之后每 schema 每 `shadowSampleEvery = 8192` 次抽查 1 次（`protocol/protox/wireshadow.go:37-42`）。整树物化用伪路径 `"*"` 计数（`protocol/protox/wireshadow.go:92-93`）。
- **失配处理（fail-safe，正确性优先于形态）**：记失配详情 → 以 oracle 结果返回本次 → 该 schema 标记降级（`WireDegraded`），后续存储点回落解码路径（`protocol/protox/wireshadow.go:27-34`、`:66-72`）。
- **失配日志携带离线复现全要素**：schema 全名、访问路径、两侧产物摘要、wire 字节 hex 转储（截断 4KB，`rawLen` 给全长）——拿到日志即可写出复现用例（`recordWireMismatch`，`protocol/protox/wireshadow.go:136-160`）。
- **任何 wire 回退必须留日志，禁止静默**：无采样保护的回退点（`MaterializeValue` / Lua 直转的 `Walk` 意外失败）走 `ReportWireFailure` 上报并降级——`ValidateWire` 已通过的字节上失败必然是扫描器 bug，静默回退会把 bug 藏起来；降级还能阻止同 schema 反复失败刷日志（`protocol/protox/wireshadow.go:162-171`；调用点 `protocol/protox/wirevalue.go:110`、`script/wire_lua.go:30`）。
- **开关与开销**：L2 是 O(路径集合) 一次性成本；L3 采样 1/8192 摊薄后可忽略；容量压测建议先开满一轮再 `SetWireShadowEnabled(false)` 稳态全关（`protocol/protox/wireshadow.go:33-35`、`:62-63`）。
- **`/debug/wire`**（挂 pprof 端口，`protocol/protox/wireshadow.go:284-302`）：输出 enabled / navigates / checks / mismatches 与降级 schema 列表（含首次失配原因）。判「零失配」看 `mismatches == 0` 且 `degradedList` 为空。

### 9.3 /debug/dedup

两个去重缓存各一个（一个 Factory 各挂一个，`protocol/protox/dedup_http.go:63-90`）：`wire` = 留存去重（字节共享）、`frozenDecoded` = 消费去重（解码共享）。判读（`protocol/protox/dedup_http.go:15-19`）：

- `hits/(hits+misses)` 为命中率——广播为主的负载应显著大于 0；
- `hits ≈ 0` 且 misses 高速增长 = 接入了逐机器人唯一的独占推送（污染，纪律被破坏）；
- `evictions` 高速增长 = 相异内容插入速率超出容量，正在挤掉可复用条目。

### 9.4 /debug/statekeys（迁移观测）

`robot.get` / `get_path` 按 key 计数（`get:` 行的 calls/tables/wireDecodes）与 `robot.get_view` 计数（`view:` 行只计 calls，`script/statekeys_http.go:11-24`、`:95-97`）。迁移某热点 key 到 `get_view` 后应看到其 `get:` 行掉零、`view:` 行上量。默认关闭（`?enable=1` 打开，`?reset=1` 分窗口）。

---

## 10. 速查：一个 S2C 推送的字节旅程

```
服务端推送字节
  → codec 解出 body（协议头无关，见 adapter-layer.md）
  → 存储点：md = Factory.MessageDescriptor(s2cProto)
      WireDegraded(md)? ──是→ 解码路径（整存 Freeze / 路径 GetFieldForStore）
      └─否→ WireSnapshot + ValidateWire
            独占响应   → NewWireValue（不进任何缓存）
            广播 ≥1KB  → Factory.WireShared（内容寻址，60 接收方共享同一字节）
  → state 整存 *WireValue / 路径映射 NavigateSegs + PlanWireRetention
  → 消费：
      robot.get 整读      → WalkWire 单遍直转 Lua 表（MaterializeAllowed 守门）
      robot.get_view      → wrapWireView 惰性视图 userdata
      await_listen 大消息 → WireShared → wrapWireView
      proto.get_path 窄读 → GetFieldCompat（缺席 message 按默认值实例下钻）
      监听脚本 on_message → RunListenScriptWire 直转
  → 每一步：首 3 次全查 + 1/8192 采样双读比对；失配 → oracle 结果 + schema 降级 + 全要素日志
```

维护清单（改动 wire 路径时逐项过）：构造点先快照后校验（§4.1）；独占推送禁入缓存（§5）；回落必须落在共享解码且必须留日志（§6、§9.2）；scratch 归还前逐元素 clear（§7）；单数字段与 map 不可早退（§8.2）；改完跑 `go test ./protocol/protox/ ./script/`（差分 fuzz + 池无污染 + 降级回落全套）。

---

## 附录 A：protocol/protox 包内文件地图

| 文件 | 职责 | 关键导出 |
| --- | --- | --- |
| `loader.go` | 扫描目录、protocompile 编译、注册 `protoregistry.Files` | `Loader` / `NewLoader` |
| `registry.go` | 消息全名/短名索引（构建后只读无锁） | `Registry` / `Lookup` |
| `factory.go` | 动态消息工厂：创建/序列化/反射读写、`GetFieldForStore`、`messageToMap` | `Factory` / `Create` / `SetField` / `GetField` / `Close` |
| `wirevalue.go` | `WireValue` 本体：wire 导航（`NavigateSegs`）、层级扫描原语、标量解码、`ValidateWire`、保留规划 | `WireValue` / `NewWireValue` / `WireSnapshot` / `ValidateWire` / `PlanWireRetention` |
| `wireview.go` | `GetField` 语义的 wire 兼容层 + 列表 Compat + 游标 | `GetFieldCompat` / `ListLenCompat` / `ListItemCompat` / `ListCursorCompat` / `WireListCursor` |
| `wirewalk.go` | WalkWire 单遍直转器（sink 接口 + scratch 池） | `Walk` / `WireTreeSink` 系 |
| `wirenav.go` | 导航路径驻留表（影子判定 + fd 链合并查表） | `navResolve`（包内） |
| `frozen.go` | 解码态不可变引用（现为降级形态） | `Frozen` / `Freeze` |
| `dedup.go` | WireCache 广播留存去重 | `WireShared` / `DedupStats` / `DedupMinBytes` |
| `dedup_frozen.go` | FrozenCache 共享解码（降级消费回落） | `ParseFrozenShared` / `FrozenDedupStats` |
| `dedup_http.go` | `/debug/dedup` 观测端点 | — |
| `wireshadow.go` | 影子验证（L2/L3）、schema 降级注册表、`/debug/wire` | `WireDegraded` / `ReportWireFailure` / `SetWireShadowEnabled` / `SnapshotWireShadowStats` |

Lua 侧出口在 `script` 包：`script/wire_lua.go`（直转器 Lua 表出口）、`script/api_proto.go`（`proto.*` 与视图 userdata 包装）、`script/api_network.go`（`await_listen` 结果形态）、`script/statekeys_http.go`（`/debug/statekeys`）。

## 附录 B：数据结构选型速记

| 需求 | 用什么 | 不要用什么 |
| --- | --- | --- |
| 每机器人独有大消息留存 | `WireValue`（字节） | 解码态任何形态（~600KB/机器人，§4.4） |
| 广播消息留存/消费 | `WireShared`（内容寻址共享字节） | 逐机器人快照；解码缓存 |
| 动作响应（请求-响应）留存 | `NewWireValue`（独立快照，不进缓存） | 任何去重缓存（污染+换血，§5） |
| 脚本只读挑字段 | wire 惰性视图（`get_view` / `await_listen` 视图） | 独占瞬态解码（029→031 证伪）；`robot.get` 整读 |
| 脚本整份加工 | `robot.get`（直转器物化） | 视图（只读、窄 API） |
| schema 影子失配后 | 降级回解码：留存 `Freeze`、消费 `ParseFrozenShared`（共享） | 独占解码回落（被证伪）；静默回退（禁止） |
