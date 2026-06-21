# 声明式 Codec 架构重构 — 总纲

> 状态：设计方案（已与负责人确认 8 项范围决策，待实施）
> 范围：`adapter` / `codec`(新) / `script` / `network` / `robot` / `engine` / `agent` / `admin` / `cmd` / `conf` / `cmd/web`(前端)
> 目标：把基于 Lua 的协议 codec（`conf/adapter/codec.lua`）替换为**纯 Go 执行的声明式 codec schema**（`codec.json`），从而让每个 Robot 的业务 Lua VM 成为**唯一被单 goroutine 触碰的 VM**，结构性消除「Lua VM 被并发踩栈」整类 bug，并删除 `luaMu` / `withReleasedMu`。

---

## 0. 背景与决策

本重构源于一个长期架构问题的根因分析，详见 [`plans/robot-lua-single-thread-design.md`](../robot-lua-single-thread-design.md)（其 §4.2 关于「adapter 用独立池」的说法已过时，其 Phase 3「删 luaMu」在当前 robot-local-adapter 架构下不成立——因为 codec 现在跑在业务 LState 上。本重构是对该问题的**正确终局解法**，并取代其 Phase 3）。

### 已确认的范围决策

| # | 决策 | 选择 |
|---|---|---|
| 1 | Lua codec 逃生舱 | **完全移除 Lua codec，只保留声明式**。业务 LState 成为唯一 VM，可彻底删 `luaMu`/`withReleasedMu`/codec 池 |
| 2 | `error.lua` | **一并迁移为 `errors.json`**，`DescribeError` 改为 Go 读 JSON map |
| 3 | 业务 Lua 的 `adapter.*` 模块 | **直接移除**（当前 `conf/scripts` 无人使用） |
| 4 | 迁移方式 | **一刀切，无兼容垫片**（遵循 `MEMORY.md`）：手工把 `codec.lua`→`codec.json`、`error.lua`→`errors.json`，同 commit 删旧文件，已存历史任务/baseline 需重新上传 |
| 5 | 计划组织 | **一份总纲 + 4 份分轨道**，每份可独立分配与回归 |
| 6 | 连接侧协程模型 | **不采用 inline decode**；Go codec 后用每连接单一 `connectionPump` 承接 decode/listen/heartbeat，避免解压/校验等 pipeline 步消耗 gnet event loop；pump 是 network 内部实现，不泄漏到 flow/engine/Lua |
| 7 | listen 缓存模型 | `ListenRef.queueSize` 显式配置小队列（缺省 1，显式 <=0 报错）；route 队列用 `map + per-route ring buffer + Mutex` 管理，不用 `sync.Map`/channel；注册属性与消费动作解耦 |
| 8 | codec 绑定粒度 + 映射 | **codec 以「连接」为单位，每连接一份 codec 文件，v1 即引入 `CodecResolver`**。连接标识 = `server` 串 `<proto>:<service>`（`parseServer`，`robot.go:769`；连接池本就按 `(transport,service)` 分，`robot.go:755/757`）。现拓扑实测 3 连接：`tcp:logic` + `tcp:battle` + `udp:battle`（`conf/flow/*.json`）。resolver = **`server串 → codec文件` 显式映射**，默认一对一（每连接一份、自解释），也允许多连接显式指向同一份去重（引擎不猜，缺映射 fail loud，无全局兜底）。每份 codec **只描述单一 transport**（offset 单向，见 §3.1）|

### 前提（范围收敛）与边界诚实性

**v1 支持范围 = 固定长度头 + 消息体 + 定长 trailer**（覆盖绝大多数游戏服务器）。

> ⚠️ **诚实标注**：这比 `CLAUDE.md`「驱动任意带类似协议头的游戏服务器」的宣传**收窄**了。以下为**已知 future gap**，宣传与实现不应脱节：
> - **变长头 / TLV / varint 长度**：不支持。
> - **握手派生密钥 / 依赖序号的加密**：不支持（密钥来自 state，不在帧内派生）。
> - **`counter`/`timestamp`/`state` 头取值源**：v1.1（见 §3.1.2，会引入每连接状态，与"无状态单例"冲突）。
>
> 遇到上述场景：通过扩 Go 算法/字段类型注册表 + 重新编译解决，而非配置。

### 重构的正当产出（动机与验收锚点）

本次以「**重构本身为目标**」立项，不拿某个 panic 当唯一理由。三项正当产出，写入验收：

1. **热路径低分配提速**：`codec.lua` 用 `string.char + ..` 拼 12B 头（每帧 ~8 次拼接 + 1 次 Lua 调用 + LState 加锁），大 body 下分配重。纯 Go 单 `[]byte` buffer + `binary.LittleEndian.PutUintX` 是实打实的低分配。**验收设量化目标**（T1 §基准）：encode/decode 的 `ns/op` 与 `allocs/op` 相对旧 Lua 路径达成明确倍率，而非只写"预期更快"。
2. **架构净简化（最大红利）**：`adapter/robot_adapter.go` **整个文件存在的唯一理由**就是"避免跨 Robot 争用 codec LState"（见其自身注释）。纯 Go 全局单例让该类**整体消失**——这不是"删一个锁"，是**删掉一整层为伺候锁而存在的间接层**（`RobotAdapter` 类 + `NewRobotAdapter` 工厂 + per-robot `r.adp` 持有）。同时 `luaMu`/`withReleasedMu` 一并删除。
3. **可视化编辑器从"顺手做"升级为产品特性**：配协议从"写 Lua"变为"填表 + 实时十六进制预览"（T3）。

### 已核对确认的事实（从"建议"升级为"已确认"）

- ✅ **`conf/scripts` 对 `bit`/`crypto`/`zlib`/`adapter` Lua 模块零依赖**（grep 实测无命中）→ T2-C 从业务 LState 删除这四个模块**安全、直接定**，无需再"建议审计"。
- ✅ **加密/压缩算法已全是 Go 实现**（`adapter/lua_crypto.go`/`lua_zlib.go` 一堆 `func`）→ T1 是**抽取 + 注册**，不是重写，风险低。
- ✅ **bcc 区域语义 / 加解密偏移非对称**已逐行核对（`lua_crypto.go:160/227`、`codec.lua:172`），已据此修正 §3.1 schema 模型。

---

## 1. 核心不变量（重构完成后必须始终成立）

> **1) 每个 Robot 的业务 LState 只被运行循环这一个 goroutine 触碰。**
> **2) codec（encode/decode/帧切割/路由键/错误码描述）是纯 Go、无状态、不可变、并发安全；不存在任何 codec LState。**
> **3) I/O 平面（gnet event loop / connectionPump）只读写线程安全对象，永不进入任何 Lua VM；gnet event loop 只切帧并入队，不做 decode pipeline。**

成立后：`luaMu` 失去存在意义（业务 VM 单一所有者）、`withReleasedMu` 失去存在意义（无需"阻塞时让别的 Lua 进来"），二者整体删除。

---

## 2. 目标架构（每个 Robot = 双平面 Actor）

```
┌──────────── I/O 平面（每连接 1 个 pump，全程纯 Go，零 Lua）────────────┐
│  gnet event loop → 只做帧切割(读固定头 Len 字段) → EnqueueInbound       │
│  connectionPump（每条连接一个 goroutine，network 内部实现）：            │
│     ├─ inbound：Go codec decode（解头/解密/解压/校验）                  │
│     │    ├─ 请求响应 → responseMap channel（唤醒阻塞的运行循环）        │
│     │    └─ 推送     → Go store callback 或 per-route listen queue      │
│     ├─ heartbeat timer/control：Go-only body builder → codec encode → Send│
│     └─ control：注册/停止心跳、stop 等 pump-owned runtime               │
└────────────────────────────────────────────────────────────────────────┘
                          │ thread-safe state.Store / listen queue
                          ▼
┌──────── 逻辑平面（唯一 goroutine = 运行循环，独占业务 LState）──────────┐
│  Executor 遍历 flow → action/boolean/Lua（全机唯一跑业务 Lua 的地方）   │
│  发包编码：Go codec 引擎 encode（不碰 Lua）                             │
│  tcpListen/udpListen：从 per-route queue FIFO 消费推送                  │
│  阻塞调用(request/listen/sleep/share)：直接阻塞本 goroutine，响应 ctx   │
└────────────────────────────────────────────────────────────────────────┘
```

**关键简化**：Go codec 引擎无状态、不可变 → **无需 per-robot adapter、无需池**。但要纠正一处过度推论：**「无状态」≠「全进程单实例」——codec 以「连接」为单位绑定，而非进程级全局。** 连接标识 = `server` 串 `<proto>:<service>`，连接池本就按 `(transport,service)` 分（`robot.go:755/757` `GetTCPConn`/`GetUDPConn`）。这条接缝在现有接线里**天然就位**：`Dialer.DialTCP/DialUDP(ctx, addr, conn, adp)` 本就逐连接传 `adp`（`gnet.go:337/343/347`）、`Connection` 存各自的 `c.adp`（`connection.go:54`，`StartDecodeLoop` 注入），`nil` 时才 fallback 到 `d.server.adp`（`:366-368`）。从「全局一份」改成「按连接 `server` 串解析 codec 再注入」是接线层小改，引擎本身不动。`robot.go` 的 `r.adp` / `NewRobotAdapter` 整体消失（纯 Go 无锁）；encode 侧改为按目标连接解析对应 codec。

> **`CodecResolver` 是 v1 必做（决策 #8），且 codec 粒度 = 每连接一份**：现拓扑实测 3 连接——`tcp:logic` + `tcp:battle` + `udp:battle`（`conf/flow/*.json`）。每条连接配一份自己的 codec 文件（哪怕 `tcp:logic`/`tcp:battle` 内容相同也分开写，连接↔文件一一对应、自解释、互不影响）。resolver = **`server串(<proto>:<service>) → codec文件` 显式映射**：
> - 默认一对一（每连接一份）；
> - 允许多连接**显式**指向同一份文件去重（如二者真相同，config 里都指 `game_codec.json`）——是否去重由 config 决定，引擎不猜；
> - **去掉 `d.server.adp` 全局兜底，缺映射 fail loud**（见 T2-C/T4）。
>
> 由「每连接一份、一份只管一种 transport」推出：codec 的加解密 **offset 简化为单向 `{encode, decode}`**（不再 tcp/udp 四元组），非对称仍保留（`udp:battle` = `{encode:11, decode:0}`）。详见 §3.1。

**连接侧协程收敛**：不采用 inline decode。虽然 inline decode 最省 goroutine，但 codec pipeline 可能包含解压、校验、hash、加密等线性/重 CPU 步，放在 gnet event loop 上会重新引入 event loop 卡顿风险。因此 I/O 平面采用**每连接单一 `connectionPump`**：gnet event loop 只切帧并入队；pump 统一处理 inbound decode、listen 分发、heartbeat timer/control。旧的 `decodeLoop + listenLoop + heartbeat goroutine` 三协程模型下线。pump 是 network 内部实现，不进入 flow/engine/Lua 配置语义；外层只感知注册监听、消费监听、注册/停止心跳。pump 必须保证 heartbeat due 优先检查 + inbound bounded batch，避免入站 backlog 饿死心跳。

**listen 缓存模型**：脚本 callback 下线后，复杂推送由主流程 `tcpListen`/`udpListen` 消费。listen 缓存从「每 route 一个槽」升级为 `ListenRef.queueSize` 指定的 per-route 小队列（缺省 1；显式 `<=0` 配置错误；队满丢最旧保最新）。`queueSize` 是注册属性，不是 `tcpListen`/`udpListen` 消费属性；消费动作 v1 每次只 FIFO pop 一条，批量消费由 flow loop 或主流程 Lua 循环实现。声明式 store listen 默认不入 queue，避免同一推送先 store 又被主流程二次处理。队列结构采用 `map[string]*listenQueue + per-route ring buffer + Mutex`，不用 `sync.Map`/channel：route 集合注册后相对稳定；队列 push/pop/丢旧是复合原子操作，仍需要队列自身锁；固定容量 ring buffer 对 `queueSize` 小队列零额外 node 分配、覆盖最旧自然、cache locality 好；channel 的「丢最旧+写最新」不是单一原子语义，且不便统计 dropped/清空/resize。

```go
type listenQueue struct {
    mu      sync.Mutex
    buf     []*Message // len == queueSize，固定容量 ring
    head    int        // 下一次 Pop 的位置
    size    int        // 当前元素数
    dropped uint64     // 队满覆盖最旧的次数
}
```


---

## 3. 共享契约（所有轨道对齐的唯一真相源）

以下三个契约由 **Track 1 冻结**，Track 2/3/4 依赖之。任何变更必须回到这里改并通知全部轨道。

### 3.0 设计总原则：物理布局 与 语义角色 分离

引擎对外契约**固定且极小**（这是工具能通用的根基，不可配置）：

- decode 必产出 `(routeKey string, body []byte, headerErr uint64)`；
- encode 必从 `(route, body, key)` 产出字节。

**这三个契约值各自的来源全部可配置**：哪段字节是 body 长、哪些字段拼 routeKey、哪个字段是错误码，统统由 schema 把"物理字段"绑定到"语义角色"。由此，过去 codec.lua 里写死的"固定内容"（`headerErr` 读 offset 4、routeKey=`cmd:act`、`index=0`、`bcc`）全部归位为**角色 + 取值源**，没有一处协议细节写死在引擎里。

> **取舍结论**：契约固定、来源全配置。没有错误码字段的协议就不绑定 `errorCode` 角色 → `headerErr` 恒 0、服务端错误码链路自然失效（正确行为）。

### 3.1 `codec.json` schema（v1）— 完整示例（现协议）

```json
{
  "version": 1,
  "endianDefault": "le",
  "frame": {
    "headerSize": 12,
    "trailerSize": 0,
    "lengthIncludesHeader": false,
    "lengthIncludesTrailer": false
  },
  "header": [
    { "name": "bodyLen", "offset": 0,  "size": 4, "type": "u32", "endian": "le", "role": "length" },
    { "name": "errCode", "offset": 4,  "size": 2, "type": "u16", "role": "errorCode" },
    { "name": "cmd",     "offset": 6,  "size": 1, "type": "u8",  "role": "route" },
    { "name": "act",     "offset": 7,  "size": 1, "type": "u8",  "role": "route" },
    { "name": "index",   "offset": 8,  "size": 2, "type": "u16", "role": "value", "source": { "kind": "const", "value": 0 } },
    { "name": "flags",   "offset": 10, "size": 1, "type": "u8",  "role": "flags",
      "bits": [ { "name": "encrypted", "bit": 0 }, { "name": "compressed", "bit": 1 } ] },
    { "name": "bcc",     "offset": 11, "size": 1, "type": "u8",  "role": "checksumOut", "from": "enc.bcc" }
  ],
  "routeKeyTemplate": "{cmd}:{act}",
  "pipeline": [
    { "op": "compress", "name": "gz", "algo": "gzip", "flag": "compressed",
      "when": { "minBodyLen": 2048, "onlySmaller": true }, "onError": "fail" },
    { "op": "encrypt",  "name": "enc", "algo": "xor_carry_rol", "params": { "rol": 3 }, "keyLen": 32, "flag": "encrypted",
      "offset": { "encode": 0, "decode": 0 },
      "when": { "requireKey": true, "minBodyLen": 1, "guards": [ { "field": "cmd", "op": "neq", "value": 0 } ] },
      "produces": [ { "name": "bcc", "algo": "xor8", "region": "ciphered" } ],
      "onError": "fail" }
  ]
}
```

> 上例是 **`tcp:logic` 连接**的 codec（TCP、encode/decode offset 均 0）。**每份 codec 只描述一种 transport**（决策 #8）；`udp:battle` 连接的 codec 另存一份，区别仅在 `offset:{ "encode": 11, "decode": 0 }`（UDP 发包前 11 字节明文、收包整体解）。
>
> 注：`encode` pipeline 正序、`decode` 反序。三处**已按源码逐行核对修正**的关键点：
> - **bcc 是密文区域明文的校验，不是整 body**：`lua_crypto.go:160/227` 为 `computeBcc(data[offset:])`，UDP(offset 11) 排除前 11 明文字节。故 bcc 建为 `encrypt` 步的**声明产物** `produces:[{name:"bcc",algo:"xor8",region:"ciphered"}]`（`region:"ciphered"`=该步实际处理的 `body[encode offset:]`），由字段 `bcc` 经 `from:"enc.bcc"` 取用。不再用「整 body 的独立 checksum 步」。
> - **加/解密偏移不对称（且按 transport 分文件后简化为单向）**：`encode_udp` offset=11（前 11 明文供服务端查密钥表），但 `decode` 恒用 offset 0（`codec.lua:172`）。每份 codec 单 transport，故 `offset` 为单向 `{encode, decode}`（缺省 0）；非对称落在 `udp:battle` 那份的 `{encode:11, decode:0}`。
> - **失败语义显式化**：每步 `onError`（`fail`|`keep`），默认 `fail` → 框架错误码（见 §3.1.4）。这是相对 codec.lua「pcall 静默保留压缩 body」的**有意改进**。
> Track 1 对拍语料**必须含 `udp:battle` 的 encode 帧（offset 11）与加密响应帧 decode（offset 0）**，否则非对称偏移会溜过去。

### 3.1.1 字段类型集（标准化、扩全）

`type` 取值（小写、规范）；多字节类型用 `endian`（`le`/`be`，缺省回退 `endianDefault`）：

| 分类 | 类型 | 说明 |
|---|---|---|
| 无符号整数 | `u8` `u16` `u24` `u32` `u64` | `u24` 用于 3 字节长度等 |
| 有符号整数 | `i8` `i16` `i24` `i32` `i64` | 二进制补码 |
| 浮点 | `f32` `f64` | IEEE-754 |
| 字节块 | `bytes`(size=N) | 定长二进制，按 `repr` 决定文本呈现（`hex`/`base64`/`ascii`） |
| 位域 | 用于 `flags` 角色字段 | 字段携带 `bits:[{name,bit}]`，pipeline 步按 `flag:"<name>"` 引用 |

> 约束：所有 header 字段 `offset+size ≤ headerSize`，互不重叠（位域共享同一整数字段除外）。变长头/varint/TLV **不支持**（超出"固定头"前提）。

### 3.1.2 字段角色 与 取值源（"固定内容"的归位）

每个字段一个 `role`，决定其 encode 取值与 decode 含义：

| role | encode（写头） | decode（读头） | 必需性 |
|---|---|---|---|
| `length` | 写最终 wire body 长（pipeline 后，口径见 `frame.lengthIncludes*`） | **decode 不读**（I/O 平面 framer 按 `BodyLength` 切帧；decode 收到的已是整帧） | **必需 1 个** |
| `route` | 取 `route[name]` | 捕获，供 `routeKeyTemplate` | **≥1 个** |
| `errorCode` | 写 0（或 source） | 作为 `headerErr` 返回引擎 | 可选（无则 headerErr=0） |
| `flags` | pipeline 按命名位置位 | 读出驱动 decode 各步 | 有 pipeline 时通常需要 |
| `checksumOut` | 写 `from`(`<step>.<output>`) 指定的 pipeline 产物 | 可选校验 | 可选 |
| `value` | 由 `source` 决定（见下） | 忽略（仅 encode 写） | 可选 |
| `reserved` | 写 0 | 忽略 | 可选 |

`role:"value"` 的 `source.kind`（把"看似固定、实则动态"的头位标准化）。**v1 仅实现 `const` 与 `route`**；`state`/`counter`/`timestamp` 仅类型留位，`schema.Validate` 在 v1 对其**直接报「v1 不支持」**——理由见 §3.1.4「无状态单例」：

| source.kind | encode 取值 | v1 | 备注 |
|---|---|---|---|
| `const` | `value` 固定值 | ✅ | `index=0` 这类占位 |
| `route` | `route[key]` | ✅ | 路由外的额外 route 派生字段 |
| `state` | `state[key]` | ❌ v1.1 | 由引擎在 encode 前预解析注入 route，避免 adapter 触碰 state |
| `counter` | 每连接自增序号 | ❌ v1.1 | **有状态**，需挂 `Connection`，破坏无状态单例 |
| `timestamp` | 当前时间 | ❌ v1.1 | 无状态但现协议不用，一并延后 |

#### 头字段不暴露给流程（v1）；`storeAs` 是发送侧机制、原样保留

> 澄清一个易混点：`storeAs`（`engine/flow.go:216`、`action.go:293`，在 `bindFields` **构建发包**时把生成的值存进 state 供后续 binding 复用）是**发送侧中间变量**机制，与 codec 无关，**原样保留、不删**。

- decode **不返回头字段**：契约固定为 `(routeKey, body, headerErr)`（见 §3.0/§3.2），`Adapter.Decode*` 签名**与今天一致、零改动**。
- 头字段中只有 `route`（→ routeKey）、`errorCode`（→ headerErr）、`flags`（驱动 pipeline）、`length`（framer 用）有结构语义；其余（`value`/`reserved`/`checksumOut`）decode 用完即弃，**不暴露给流程**。
- 若将来某协议真需要"流程读头字段"（服务端在头里塞序号/id/状态位），再以显式机制（decode 增可选头字段返回）补——**v1.1，不为不存在的需求预建**。

### 3.1.3 pipeline 与 算法注册表（扩全）

pipeline 为有序步骤，`op ∈ {compress, encrypt, checksum, hash}`；每步：

- `name`：步骤名（供 `flag`/`from`/`when.appliesWith` 引用）。
- `flag`：绑定到 `flags` 字段的某个命名位，记录该步是否应用；decode 时据位决定是否执行。
- `when`：**结构化**条件（不引入字符串 DSL）：`minBodyLen` / `onlySmaller` / `requireKey` / `guards:[{field,op,value}]`（`op ∈ eq/neq/gt/gte/lt/lte`）/ `appliesWith:"<stepName>"`。
- `offset`（encrypt）：单向子对象 `{encode, decode}`，缺省 0（每份 codec 单 transport，无需 tcp/udp 区分）。cipher 只处理 `body[offset:]`，前缀保持明文（供服务端查密钥表）。**encode/decode 偏移独立**，复刻 codec.lua 的非对称语义（`udp:battle` = `{encode:11, decode:0}`）。
- `produces`（任意步，主要 encrypt）：声明该步的**派生产物** `[{name,algo,region}]`。`region ∈ {ciphered(=该步处理的 body[offset:]), bodyPlain, bodyFinal, header, frame}`。产物经字段的 `from:"<step>.<name>"` 写入头位（如 bcc）。**这取代了"整 body 独立 checksum 步"**——校验的作用域就是绑定步实际处理的那段，模型上不再脱节。
- `over`（独立 checksum/hash 步，用于与 cipher 无关的全帧校验）：`bodyPlain` / `bodyFinal` / `header` / `frame` / `{rangeStart,rangeEnd}` 字节区间。
- `onError`：decode 侧失败策略 `fail`(默认，→框架错误码) | `keep`(best-effort 保留原样)。
- `params`：算法参数。

**算法注册表（v1 目标，迁移自 `adapter/lua_crypto.go`+`lua_zlib.go`，它们已是 Go 实现）**：

| 注册表 | v1 内置 |
|---|---|
| cipher | `none` `xor`(单字节/循环 key) `xor_carry_rol`(现协议) `rc4` `aes_cbc` `aes_ctr` `aes_ecb` `xxtea` |
| compress | `none` `gzip` `zlib`(deflate) `snappy` `lz4` `zstd` |
| checksum | `none` `xor8`(bcc) `sum8` `crc16` `crc32` `crc32c` |
| hash | `md5` `sha1` `sha256`（+ HMAC 变体，供头部摘要类协议） |

> v1 至少落地"现协议所需 + lua_crypto.go 已有"的子集（`xor_carry_rol`/`rc4`/`aes_*`/`xxtea`/`crc*`/`xor8`/`gzip`），其余（`snappy`/`lz4`/`zstd`/`zlib`）按需补；注册表设计为开放接口，新增算法 = 实现接口 + 注册一行（前端算法清单同步，见 T3）。

### 3.1.4 引擎语义契约（前端/后端共同遵守）

- 帧 = `header(headerSize)` + `body` + 可选 `trailer(trailerSize)`；长度字段口径由 `frame.lengthIncludesHeader/Trailer` 决定（物理位置见 Header 中唯一的 `role:"length"` 字段）。
- decode 产物为 `(routeKey, body, headerErr)`：`routeKey`=`routeKeyTemplate` 代入 `route` 角色字段；`body`=管线还原后的有效载荷；`headerErr`=`errorCode` 字段值（无则 0）。头字段不外泄（见 §3.1.2）。
- encode 输入固定为 `(route, body, key)`：`route` 为 `ActionDef.route` 反序列化对象，按字段 `name`/`source` 取值。
- **失败语义（通用引擎必须显式；受 3-tuple 签名约束，不返回 err）**：decode 任一 pipeline 步失败（解压失败 / 解密后异常 / `checksumOut` 校验不过），按该步 `onError` 处理：`fail`(默认) → **返回空 `routeKey`**（帧被 `decodeAndDispatch` 丢弃 + warn，等同今天 `connection.go:487` 行为；挂起请求走 responseMap 超时），**绝不把乱码当 body 塞进 state**；`keep` → 保留原字节继续（复刻 codec.lua 旧行为，需显式声明）。**decode 不返回 err**（签名零改动，见 §3.2），故失败以"丢帧"而非"框架 ActionError"表达；若 v1.1 想要框架级 decode 错误可见（monitoring/errcode），再考虑加 err 返回。
- **方向语义（encode/decode 控制流不对称，已据 `codec.lua:171/176` 坐实）**：
  - **encode**：每个 pipeline 步先求值其 `when`（`minBodyLen`/`onlySmaller`/`requireKey`/`guards`）；**仅当通过才执行，并把其 `flag` 命名位置位**。即 `when` 是**纯 encode 决策**，结果**记录进 flag 位**。
  - **decode**：每步是否执行**只看其 `flag` 命名位是否在解码出的 flags 中置位**（encrypt 另要求运行时 key 在场）——**不重新求值 `when`/`guards`/`minBodyLen`/`onlySmaller`**。codec.lua `decode_tcp` 正是 `if band(flags,FLAG_ENCRYPT)~=0`（:171）/ `if band(flags,FLAG_COMPRESS)~=0`（:176）驱动，**从不**重判 cmd!=0 或 body 长度。
  - **由此推导的强校验（`schema.Validate`）**：凡带 `when`（执行有条件）的步**必须绑定 `flag`**，否则 encode 的决策无处记录、decode 无法复现；每个命名 flag 位**至多被一个步绑定**。无 `when` 的无条件步（两向恒执行）不需要 flag。
  - **长度字段方向**：`role:"length"` 字段 encode 写"pipeline 后的 wire body 长"（口径由 `frame.lengthIncludesHeader/Trailer` 决定：默认 false = 纯 body 长，不含头/尾）；该字段**仅供 I/O 平面 framer（`BodyLength`）切帧**，**decode 不再读它**（decode 收到的已是整帧，body = `data[headerSize : len-trailerSize]`）。其物理位置（offset/size/type/endian）只在此 `role:"length"` 字段上声明一次，`frame` 不再重复（消除双声明漂移）。
  - **头部缓冲零初始化**：encode 分配 `headerSize` 字节缓冲并整体置零后再按字段写入；未被任何角色/产物写入的字节（reserved、未生效步的 `checksumOut`）**恒为 0**。`checksumOut.from` 指向的产物若该步未执行（未产生）→ 字段写 0。此为字节级对拍的必要条件（codec.lua `_do_encode` 显式写出全部 12 字节，等价语义）。
  - **产物计算时机（按 `region`）**：`ciphered` = 在所属步执行时对其处理区域 `body[offset:]` 算；`bodyPlain` = pipeline 执行前（原始 body）；`bodyFinal` = 全部 pipeline 步之后；`header`/`frame` = 头已写就后。产物先暂存，header 写入阶段由 `checksumOut` 字段经 `from` 取用。

> **设计目标的重述（命题适用域）**：重构的目标**不是「复刻 codec.lua」**（那只是对拍验收手段），而是**让 schema 在结构上无法表达「同一份 codec」一份自相矛盾的双向配置**——即任一 codec 的 encode 与 decode 互不一致的配置，在 `schema.Validate` 阶段就被拦下。此保证是**单份 codec 内的**：它约束「一份 codec 自身双向一致」，**不**跨 codec 文件（不同连接的 codec 各自独立满足，互不相干）。codec 既已按连接（`<proto>:<service>`）分文件、每份单 transport（见 §2/决策 #8），「TCP 与 UDP」天然落在不同 codec 文件里，不在这道单份保证的射程内。

> v1 边界已定：`trailer` 纳入（默认 size=0）；`hash` 步注册表留位、引擎支持但现协议不用；`storeAs` 是发送侧机制、原样保留（不删）；头字段 `expose`/`headerFields` **不进 v1**（延后 v1.1）；`counter`/`timestamp`/`state` 取值源 v1.1（见 §3.1.2）。

### 3.2 Go `Adapter` 接口（**签名零改动**）

`adapter/adapter.go` 现有 9 方法**签名一字不变**——decode 仍 `(routeKey, body, headerErr)`、encode 仍 `(route, body, key) → 字节`。砍掉 `expose`/`headerFields` 后，整个 refactor 里**唯一可能动签名的地方被消除**，破坏面收窄到只剩"绑定拓扑（per-服务）+ codec 实现（Lua→Go）"。

```go
// 纯 Go、无状态、不可变、并发安全 → 按连接持有实例（每连接 server串 一份 codec、可显式共享同一份），任意 goroutine 并发调用无锁
func NewSchemaAdapter(schema *CodecSchema, errorMap map[uint64]string) (Adapter, error)

// 签名与今天（adapter/adapter.go）完全一致：
DecodeTCP(data []byte, secretKey []byte) (routeKey string, body []byte, headerErr uint64)
DecodeUDP(data []byte, secretKey []byte) (routeKey string, body []byte, headerErr uint64)
EncodeTCP(route any, body []byte, secretKey []byte) []byte
EncodeUDP(route any, body []byte, secretKey []byte) []byte
// HeaderSize / BodyLength / ExpectedRouteKey / Close / DescribeError 同现状
```

因实现并发安全，`DecodeTCP`/`EncodeTCP`/`ExpectedRouteKey` 可被 `connectionPump` / 主 goroutine / 心跳 Go builder 任意并发调用，**无需任何锁**。

### 3.3 `errors.json` 格式

```json
{ "0": "成功", "1": "数据库错误", "2": "协议解析错误", "19": "消息解密失败" }
```

扁平 `code(string) → 描述(string)`。`DescribeError(code uint64)` 改为 Go map 查找（未命中返回空串或 `未知错误码:N`，由 Track 1 定）。

---

## 4. 轨道划分与依赖

```
        ┌─────────────────────────────────────────────┐
        │  Track 1: Go codec 引擎 + 算法注册表 +        │  ← 冻结 §3 契约
        │           errors.json（无对外行为变更）        │     可立即独立开工
        └───────────────┬─────────────────────────────┘
                        │ 提供 NewSchemaAdapter / schema 格式 / errors.json
        ┌───────────────┼───────────────┬───────────────┐
        ▼               ▼               ▼               
 ┌─────────────┐ ┌─────────────┐ ┌─────────────┐
 │ Track 2:    │ │ Track 3:    │ │ Track 4:    │
 │ 后端集成 +  │ │ 前端 codec  │ │ 配置 & 分发 │
 │ 删 luaMu    │ │ .json 编辑  │ │ + conf 迁移 │
 └─────────────┘ └─────────────┘ └─────────────┘
   依赖 T1         依赖 T1(schema)   依赖 T1(文件名/loader)
                   + T4(文件名/端点)
```

| 轨道 | 文件 | 负责范围 | 依赖 | 可否并行 |
|---|---|---|---|---|
| **T1** | [`01-track-codec-engine.md`](01-track-codec-engine.md) | 新 Go codec 引擎、字段类型集、算法注册表（迁移 `lua_crypto.go`/`lua_zlib.go`/`helpers.go`）、`errors.json` 加载、`NewSchemaAdapter`、对拍测试 | 无 | 立即开工，**关键路径** |
| **T2** | [`02-track-backend-integration.md`](02-track-backend-integration.md) | listens 主流程消费化 + per-route queue、flow action 声明式心跳、connectionPump、用 Go adapter/CodecResolver 替换 RobotAdapter、从业务 LState 移除 codec/adapter 模块/crypto/zlib/bit、删 `luaMu`+`withReleasedMu`、心跳/收发/密钥重新接线 | T1 | T1 冻结契约后开工 |
| **T3** | [`03-track-frontend.md`](03-track-frontend.md) | `codec.json` 编辑器、`resourcesStore`/`baselineApi`/`taskActions` 改造、校验模型、`error.lua`→`errors.json` UI、路由键真实计算 | T1(schema) + T4(文件名/端点) | schema 冻结后开工 |
| **T4** | [`04-track-config-distribution.md`](04-track-config-distribution.md) | CLI flag、Admin multipart/baseline/下载、agent 下载与加载、`conf/adapter` 文件迁移、loader 接线 | T1(loader 签名) | T1 冻结后开工 |

### 建议节奏

1. **先止血（可选 Phase 0，偏配置/脚本）**：按 T2-A 下线 listen 脚本 callback；复杂推送改由主流程 `tcpListen`/`udpListen` 消费，简单状态推送可用声明式 store，**当场消灭线上 `framework/53` panic**。
2. **T1 冻结 §3 契约**（schema/接口/errors.json），其余三轨道据此并行。
3. T2/T3/T4 并行推进，最后**合流切换 + 删旧**（一刀切 commit）。

### T1 Go codec 引擎实施切片总览

T1 是关键路径，必须先冻结 `codec.json` schema、`errors.json`、`NewSchemaAdapter`、loader 签名与算法元数据，T2/T3/T4 才能稳定并行。

1. **Schema 类型与加载**：新建 `codec/` 包，定义 `CodecSchema`、`FrameSpec`、`Field`、`ValueSource`、`PipelineStep`、`StepOffset`、`StepProduce`、`OverSpec`、`StepCond` 等类型；JSON tag 使用 camelCase；`LoadSchema` 只读 `codec.json` + JSON parse + `Validate`，不兼容旧 `codec.lua`。
2. **Schema.Validate**：校验 headerSize/trailerSize/endian、字段唯一/不越界/不重叠、类型与 size 匹配、必有且仅有一个 length、至少一个 route、routeKeyTemplate 占位合法、pipeline step/flag/algo/from/appliesWith 引用合法；带 `when` 的 step 必须绑定 flag；v1 拒绝 `state/counter/timestamp` 头取值源。
3. **编译层 compile.go**：`NewSchemaCodec` 把 schema 编译成不可变产物；预解析 length/route/errorCode/flags/checksumOut/value 字段、routeKey 模板、flag mask、from/produces 引用、算法实现；热路径不做字符串解析和 map 查找。
4. **算法注册表与元数据**：建立 cipher/compressor/checksum/hasher 注册表；先落 `xor_carry_rol`、`gzip`、`xor8`、`none`，再迁移 `xor`、`rc4`、`aes_*`、`xxtea`、`crc*`、`md5/sha1/sha256`；每个算法导出 `{name,op,params,description}` 元数据供 T3/T4 使用。
5. **Encode**：pipeline 正序，`when` 只在 encode 判断；compress 支持 `minBodyLen/onlySmaller`；encrypt 支持 `requireKey/keyLen/guards/encode offset`；`produces` 按 region 计算；header 零初始化，length 写 pipeline 后 body 长，flags 写累计命名位，checksumOut 写产物。
6. **Decode**：保持 3-tuple `(routeKey, body, headerErr)`；pipeline 反序，是否执行只看 flags，绝不重算 `when/guards/minBodyLen/onlySmaller`；decode offset 与 encode offset 独立；`onError=fail` 返回空 routeKey 且 body 不外泄，`keep` 保留当前字节继续。
7. **Adapter 与 errors.json**：`adapter.NewSchemaAdapter` 是薄封装，满足现有 9 方法；`Close` no-op 且幂等；`LoadErrorMap` 读取 `errors.json` 的 `code→中文描述` map；`errors.json` 可选可共享，每个声明连接的 codec 文件必需。
8. **Admin preview 支撑**：T1 暴露纯计算 preview helper，供 T4 的 `POST /sbot/codec/preview` 调用；支持 encode/decode 输入 schema、route/body/key/frame 和 tcp/udp，返回 frame/body/header 字段解释、routeKey、headerErr。
9. **测试与对拍**：validate 畸形 schema 单测、算法单测、旧 `LuaAdapter` vs 新 `SchemaCodec` 字节级对拍；必须覆盖 TCP/UDP、加密、压缩、cmd=0、headerErr、BodyLength、`udp:battle` codec 的 encode offset=11 / decode offset=0、坏 gzip/checksum/缺 key 的 fail/keep 语义、并发 `-race` 与 benchmark。
10. **冻结与交接**：提交当前协议各连接 `*_codec.json`/共享 `errors.json` 迁移产物；冻结 schema、loader、adapter、算法元数据接口；通知 T2 Adapter 签名零改动且并发安全，通知 T3 schema/算法元数据可用于编辑器，通知 T4 文件名/loader 签名可接线。

### T2 后端集成实施切片总览

T2 必须按 **2-A → 2-B → 2-C → 2-D** 顺序推进；2-D 删除锁前必须通过异步 Lua 零残留审计。

#### T2-A：listen 主流程消费化 + per-route queue

1. **Schema/校验**：`ListenRef.queueSize *int`，缺省 1，显式 `<=0` 配置错误；`ListenDef.script` 禁用；同一连接同一 `routeKey` 重复注册必须完全一致，否则 fail loud。
2. **Network queue**：以 `listenQueue` 固定容量 ring buffer 替换单槽 `listenMsg`；`Push` 队满覆盖最旧并统计 dropped；`GetListenResp` FIFO pop，一次一条。
3. **Robot 注册**：静默/缓存 listen 注册 queue；`s2cProto + store` 注册 Go store callback 且默认不入 queue；删除脚本 callback 分支。
4. **NetSender 接线**：`EnsureTCPListener` / `EnsureUDPListener` 增加 `queueSize int`；`queueSize` 只来自注册侧 `ListenRef`，`tcpListen`/`udpListen` 不带容量。
5. **配置迁移**：先 ranked/team，再 guild；复杂推送由主流程 `tcpListen`/`udpListen`/`network.wait_listen` 消费，原 callback 逻辑合并回主流程脚本。
6. **验收**：`ListenDef.script` callback 用途归零；queueSize=1/2 的 FIFO/覆盖/dropped 可验证；`framework/53 ... listen_*` 归零。

#### T2-B：flow action 声明式心跳

1. **Action schema**：新增 `tcpHeartbeat` / `udpHeartbeat`；必填 `service`、`intervalMs`、`route`；可选 `skipWhenMissing`；`intervalMs<=0` 报错。body 构造**双模式**（互斥，覆盖通用游戏服心跳的三类主流形态——stressbot 是通用工具，不止一个游戏）：
   - **proto 模式**（主流，现代 protobuf 游戏服）：配 `c2sProto` + `bindings`，复用现有 `tcpSend` 的 proto 构建机制（factory + bindFields）→ proto body → adapter 编码。大多数游戏服心跳的形态。
   - **raw-binary 模式**（C++ 自研协议服 / 实时战斗同步，如本项目 battle 心跳——无 proto、wire format 非 protobuf）：配 `heartbeatFields`（声明式 raw-LE 布局：`{type:u8|u16|u32|u64|i8|i16|i32|i64, source:fixed|state|stateCounter|counter|timestamp|randomInt, ...}`），由 `engine.BuildHeartbeatBody` Go-only 打包 → adapter 编码。
   - **空 body**：两者都不配 = 只发头+路由（轻量心跳）。
   - 校验：`c2sProto` 与 `heartbeatFields` **互斥**；同时配报错。
2. **源子集**：proto 模式 bindings 复用现有 binding 解析；raw-binary 模式 source 子集为 `fixed`/`state`/`stateCounter`(共享计数器自增，如 packageIndex)/`counter`(心跳私有计数器)/`timestamp`/`randomInt`——只覆盖心跳构造所需，不开放完整随机/列表/map binding；禁止 Lua 条件。
3. **注册语义**：heartbeat action 只注册/更新 runtime，不等待发送；每 tick 不计入该 action 的网络延迟样本。
4. **Go-only builder**：builder 只返回 `(body, skip, err)`；不捕获 Lua LState，不调用 Redis/HTTP/network request；network/pump 负责 secretKey、codec encode、send。
5. **过渡策略**：可先让旧 heartbeat goroutine 调 Go-only builder，实现“心跳不进 Lua”；2-C pump 落地后再合并 timer/control。
6. **旧 API 下线**：删除保存 Lua function 的 `register_*_heartbeat(..., builder_fn)` 能力；`TryLock(luaMu)`、`withReleasedMu`、`CallByParam` 心跳路径归零。
7. **验收**：主流程阻塞 listen/Lua/share/sleep 时心跳仍发送；fixed/state/counter/timestamp 对拍旧输出；停止无 goroutine 泄漏。

#### T2-C：codec 移出业务 LState + connectionPump

1. **Adapter 签名不变**：9 方法保持现状；Go `NewSchemaAdapter` 替换 Lua 实现；decode 仍 3-tuple，不做 expose/headerFields。
2. **CodecResolver**：`Resolve(server string)`（`<proto>:<service>`）显式映射 连接→adapter；替换 `ManagerConfig.Adapter`、`Robot.adp`、`NewRobotAdapter`；缺映射 fail loud，无 fallback。
3. **Dial/decode**：拨号前按连接 `server` 串解析 adapter 并固定到 `Connection`；删除 `Dialer.dial` 的 server adapter 兜底；decode 生命周期内不再查 resolver。
4. **Encode/连接接线**：`ActionExecutor`、`protocolEncode`、`ExpectedRouteKey`、listen routeKey、heartbeat encode 全部按目标连接（`server` 串）解析 codec。
5. **script API 清理**：Lua network API 仍能发包/请求/监听，但 encode/decode/routeKey 由 Go adapter 完成；删除 `*Locked` codec 调用与 `adapter.*` Lua 模块。
6. **业务 LState 瘦身**：删除 `RobotAdapter`；业务 LState 不再注入 `adapter`/`bit`/`zlib`/`crypto`。
7. **connectionPump 替换旧三协程**：gnet 只切帧入队；pump 处理 decode、request-response、listen store/queue、heartbeat timer/control；删除 `decodeLoop`、`listenLoop`、独立 heartbeat goroutine。
8. **生命周期**：Close/ctx.Done 触发 pump 退出；`RegisterListen`/`RegisterHeartbeat` 在关闭后明确报错；inboundCh 满关闭连接释放压力。
9. **验收**：`RobotAdapter`、`NewRobotAdapter`、`loadAdapterModule`、`*Locked`、`Context.Adapter` 旧类型、Dialer fallback 归零；多连接（`tcp:logic`/`tcp:battle`/`udp:battle`）使用正确 codec；长跑无旧 goroutine 泄漏。

#### T2-D：删除 `luaMu` / `withReleasedMu`

1. **前置闸门**：2-A/2-B/2-C 完成后，全仓审计异步 goroutine 中访问 `*lua.LState` / `luaPool.Run*` / `L.CallByParam` 的路径；只允许 Robot 主流程 goroutine 执行业务 Lua。
2. **删除释放锁阻塞模型**：network/share/sleep 等 Lua API 直接阻塞当前 Robot 主流程 goroutine，并响应 `ctx.Done()`；不再释放/重获锁。
3. **删除 Context/Robot 锁字段**：删除 `withReleasedMu`、`script.Context.LuaMu`、`Robot.luaMu`、所有 `Lock/Unlock`；不保留空实现兼容。
4. **RuntimePool 收敛**：`RunActionScript` / `RunBooleanScript` 同步调用；旧 `RunCallbackScript` 无调用后删除，避免未来误用。
5. **停止路径简化**：Stop 只取消 ctx、关闭连接、等待 pump/主流程退出；不再等待 Lua builder 释放锁。
6. **日志清理**：删除 `luaMu`、`withReleasedMu`、`TryLock`、释放/获取 Lua 锁相关日志与注释。
7. **验收**：静态清零锁/旧异步模型；阻塞 Lua API 可被 ctx 取消；connectionPump 在主流程阻塞期间继续处理响应/推送/心跳；长跑无 Lua panic。

### T3 前端实施切片总览

T3 的目标是把“Lua 适配器资源”改成“协议配置资源”，并同步 T2/T4 的新 flow 字段、任务上传和校验契约。UI 文案统一使用「协议配置」，避免暴露 codec/schema/adapter 等技术术语。

1. **资源存储与文件名切换**：IndexedDB key 从 `codec.lua`/`error.lua` 改为 `codec.json`/`errors.json`；`ResourceType` 可继续用内部 `'adapter'` 分类，但 UI 展示为「协议配置」；删除 Lua 函数清单校验，不做旧 key 自动迁移。
2. **类型与校验模型**：新增 `src/types/codec.ts` 镜像 T1 schema；`validateCodecSchema` 做前端结构校验（JSON、字段越界/重叠、role 数量、pipeline 引用），深层语义以后端 Go 校验/预览为准；`adapterMissing` 语义改为协议配置错误集合。
3. **协议配置可视化编辑器**：ResourcesDrawer 的适配器 tab 改为「协议配置」tab；结构化编辑器为主，源码 JSON 为辅；包括 header/trailer 字节条带、字段表、role 联动表单、pipeline 卡片、routeKey 模板、`errors.json` code→中文表格。
4. **算法清单与实时预览**：前端通过服务层调用 `GET /sbot/codec/algorithms` 与 `POST /sbot/codec/preview`；算法下拉和 encode/decode 预览走后端真实 Go 引擎，不在前端维护算法实现或伪 fallback。
5. **baseline / 任务上传 / 启动**：baseline 路径与 multipart 字段改为多份 `adapter/<proto>_<service>_codec.json` + 共享 `adapter/errors.json`；diff language 改 JSON；任务启动硬校验 flow 引用连接均有对应协议配置；不上传旧 `.lua` 文件。
6. **FlowEditor 同步 T2 新契约**：支持 `ListenRef.queueSize` 校验；禁用 `ListenDef.script` callback 配置入口；新增 `tcpHeartbeat` / `udpHeartbeat` 表单；heartbeat bindings 只开放 `fixed`/`state`/`counter`/`timestamp`。
7. **routeKey 真实计算与 listen 校验**：基于当前连接对应 `*_codec.json` 的 `routeKeyTemplate` 与 route 字段计算真实 routeKey；替换 JSON 排序伪 key；协议配置缺失/错误时提示先修复，不静默 fallback。
8. **旧术语清理**：删除 Lua API 文档中的 `adapter` 模块、popover 颜色项与测试；资源面板、启动弹窗、baseline 弹窗、README 全部改成按连接的多份 `*_codec.json`/共享 `errors.json`/「协议配置」。
9. **验收**：`npx tsc -b` 与 Vitest 通过；协议配置 tab 可编辑/导入/保存/清除；结构化视图与源码 JSON 双向同步；任务上传路径正确；前端无 `codec.lua`/`error.lua`/adapter Lua 模块残留。

### T4 配置与分发实施切片总览

T4 负责把文件名、加载顺序、连接(server)→codec 显式映射、Admin/Agent/standalone 分发链路钉死，为 T2 的 `CodecResolver` 和 T3 的上传/编辑提供稳定边界。

1. **文件形态**：`conf/adapter/` 下**每连接一份** codec 文件，命名 `<proto>_<service>_codec.json`（如 `tcp_logic_codec.json`/`tcp_battle_codec.json`/`udp_battle_codec.json`）；`errors.json` 可共享一份（错误码描述与 transport 无关）；`-adapter` CLI flag 语义保持“目录”；任务包按文件路径分发。
2. **统一加载顺序**：读取 config → 枚举 config/flow 声明的连接（`server` 串）→ 按映射读取各自 codec 文件（+`errors.json`）→ `schema.Validate` → `adapter.NewSchemaAdapter` → 为每个连接显式填入 `CodecResolver` → 加载 proto → 加载 flow → 校验 flow 中 `server` 引用均可 Resolve。
3. **连接→codec 显式映射**：默认每连接一份 codec 文件；多连接可**显式**指向同一份文件去重（resolver 编译时 dedup 为同一 adapter 实例）；必须在 resolver map 中按 `server` 串逐项登记；不做 runtime fallback。
4. **迁移产物**：手工/脚本辅助把单一 `codec.lua` 按连接拆/转成多份 `*_codec.json`，把 `error.lua` 转成共享 `errors.json`，用 T1 对拍验证后同切换 commit 删除旧 `.lua`。
5. **standalone 接线**：`cmd/agent` 从 adapter 目录按连接加载多份 `*_codec.json` 并构建 `CodecResolver`；`ManagerConfig.Adapter` 改为 `ManagerConfig.CodecResolver`；`Dialer` 不再持 server-level fallback adapter；路径解析单测同步。
6. **Agent 接线**：默认路径与下载文件改成 `adapter/` 下各连接 codec 文件（+`errors.json`）；task runner 加载后按任务声明的连接（`server` 串）构建 resolver；flow 引用未登记连接时任务启动阶段失败并上报明确错误。
7. **Admin 分发**：multipart 字段、configFiles 清单、baseline 落盘、baseline HTTP 端点全部改成多份 `*_codec.json` + 共享 `errors.json`；Admin 任务保存只存发文件，可做 JSON 语法校验，不执行任务 codec 加载。
8. **预览端点**：Admin 提供 `POST /sbot/codec/preview` 与 `GET /sbot/codec/algorithms`，两者纯计算、不入库、不下发，供 T3 编辑器调用真实 Go 引擎。
9. **切换节奏**：T1 先落新引擎和迁移产物；T4 先落文件名/加载/任务包双端准备但不删旧文件；T2 切到 `CodecResolver`；T3 切到 JSON 编辑上传；最后单独合流 commit 删旧 Lua codec。
10. **验收**：standalone/agent 均用 JSON codec 启动成功；每个声明连接（`server` 串）都能 Resolve；flow 引用未登记连接报错清晰；任务包路径大小写一致；全仓 `codec.lua`/`error.lua`/`NewLuaAdapter` 零残留（历史 plan 除外）。

---

## 5. 全局验收标准

遵循 `CLAUDE.md` 验证流程，逐项确认：

- [ ] `go build ./...` 通过；`cd cmd/web && npx tsc -b` 通过；`npm run test` 通过。
- [ ] 新增 Go codec 引擎对拍测试：对一批真实帧，`NewSchemaAdapter` 的 encode/decode 与旧 `codec.lua` **字节级一致**（含加密、压缩、`udp:battle` codec 的 encode offset=11 / decode offset=0、bcc、headerErr、routeKey、BodyLength）。
- [ ] `conf/adapter/codec.lua`、`error.lua` 已删除，代之以 `codec.json`、`errors.json`；全链路（standalone / Admin / agent / 前端）无残留对 `.lua` codec 的引用。
- [ ] 全仓搜索 `luaMu`、`withReleasedMu`、`NewRobotAdapter`、`RobotAdapter`、`loadAdapterModule`、`EncodeTCPLocked`、`decodeLoop`/`listenLoop`/独立 heartbeat goroutine 等旧异步模型：**零残留**（或仅注释/历史 plan）。
- [ ] 长跑 1–2 小时：零 `framework/53` 与 nil-pointer panic；多排入队成功率 ≥ 现状；心跳按间隔稳定、字段正确；推送字段正确落地。
- [ ] 内存：每 Robot 业务 LState 较现状下降（实测基线 ~98KB → ~80KB，省下 codec 模块）；无 codec LState。

---

## 6. 风险与回滚

| 风险 | 缓解 |
|---|---|
| schema 表达力不足以复刻 `codec.lua` | T1 先做**对拍测试**（字节级 diff），不通过不切换 |
| 一刀切导致历史任务/baseline 失效 | 已确认接受；T4 提供 `codec.lua`→`codec.json` 的人工转换产物并同 commit 提交 |
| 删 `luaMu` 过早引入新并发 bug | 严格顺序：T2 必须在「codec 移出业务 VM + listens/心跳声明式化 + connectionPump 不进 Lua」全部完成后才删锁；删前全仓审计无异步 Lua 路径 |
| connectionPump 中 inbound backlog 延迟心跳 | pump 设计强制 heartbeat due 优先检查 + inbound bounded batch；压测观察心跳 jitter 与连接掉线率 |
| listen 小队列配置过小导致事件被丢 | `queueSize` 默认 1 只适合状态覆盖型；事件型 route 必须显式配置容量，队满丢最旧保最新并记录 metric/debug |
| 前后端文件名/端点不一致 | 文件名 `*_codec.json`/`errors.json` 与端点由本总纲 §3 + T4 统一钉死，T3 引用 |
| 连接枚举不完整导致运行时才发现 codec 缺失 | T4 loader 从 config/flow 声明的连接（`server` 串）显式构建 resolver，并在加载 flow 后校验所有 `server` 引用均可 Resolve，启动阶段 fail loud |
| 前端可视化编辑器一次性过大 | T3 按切片先落资源 key/上传/源码 JSON 校验，再落结构化 editor 与预览；保证每步可编译可回归 |
| routeKey 真实计算依赖协议配置 | 协议配置缺失/错误时停止 listen 去重增强并提示修复，不静默 fallback 到伪 key |
| `routeGuard`/字段类型覆盖不足 | v1 仅实现 `codec.lua` 实际所需集合；其余按需扩注册表（已接受需改 Go） |

**回滚**：四轨道分别多 commit、可独立回滚；最危险的「合流切换 + 删旧」单独成一个 commit，回滚即恢复 Lua codec（在切换前 `codec.lua` 仍在仓库历史中）。

---

## 7. 待实施时仍需确认/细化的开放问题（非阻塞）

字段类型集（§3.1.1）、角色/取值源（§3.1.2）、算法注册表（§3.1.3）、作用域/方向/失败语义（§3.1.3-4）均已规范化并核对源码修正，剩余细化项：

1. ~~`trailer`~~ **已定**：纳入 v1，默认 `trailerSize:0`，引擎分帧预留"头+体+尾"口径由 `frame.lengthIncludesHeader`/`frame.lengthIncludesTrailer` 决定（`lengthField` 对象已删除，length 字段物理位置只在 `role:"length"` 头字段声明一次）。
2. ~~`storeAs`~~ **不删**：澄清 `storeAs` 是**发送侧**中间变量机制（`flow.go:216`/`action.go:293`，构建发包时存值供后续 binding 复用），与 codec 无关，**原样保留**。「`expose`/`headerFields`」（decode 返回头字段给流程）**不进 v1**——现协议无需读头字段（routeKey+headerErr 已够），延后 v1.1。
3. ~~`hash` 步~~ **已定**：注册表 + 引擎执行 v1 支持，现协议不用（bcc 走 `xor8` produces）。
4. ~~`counter`/`timestamp`/`state` 取值源~~ **已定收窄**：v1 `schema.Validate` 直接拒绝（报「v1 不支持」），保住「无状态共享单例」主张；v1.1 再议（`counter` 需挂 `network.Connection`，`timestamp` 虽无状态但现协议不用）。
5. 前端编辑器形态：**v1 直接做可视化结构化编辑器**（字节布局表 + pipeline 步骤卡片 + 实时 hex 预览，见 T3 §2.2），Monaco JSON 作为「高级/源码」次级视图并存。
6. `DescribeError` 未命中返回值约定（建议 `未知错误码:N`）。
