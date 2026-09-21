# 声明式协议编解码（protocol/codec）

> 本文对照实际代码编写，反映已实施状态（核对基准日 2026-09-20）。文内所有 `文件:行号` 均指向当前源码。

## 1. 概述

协议编解码完全由声明式配置驱动：每条命名连接一份 `*_codec.json` 描述协议头布局、路由键、
压缩/加密/校验管线与可选心跳，共享一份 `errors.json` 提供业务错误码描述。配置在加载期
一次性编译为不可变的 `SchemaCodec`（`protocol/codec/compile.go:221-232`），运行时执行层是
纯 Go——帧切割与编解码热路径零脚本调用、零字符串查表（字符串→索引/掩码/算法实现全部在
编译期预解析，compile.go:6-8）。声明式的收益：切换目标服务器只换 JSON 配置、`.proto` 与
flow.json，Go 代码零改动；配置错误在启动期以中文错误显式失败，不留运行期静默兜底。

数据流（收发各一条）：

```
发送：flow.json route → ActionExecutor 构建 protobuf body
      → adapter.EncodeTCP/UDP(route, body, secretKey) → gnet 发送
接收：gnet OnTraffic → HeaderSize()/BodyLength() 帧切割（纯 Go 缓存逻辑）
      → 投递 inboundCh → connectionPump 调 DecodeTCP/UDP
      → (routeKey, body, headerErr) 分发给请求-响应匹配或持久化监听
```

包内分工：`protocol/adapter.go` 定义接口，`protocol/codec_resolver.go` 负责发现/加载/组装，
`protocol/schema_adapter.go` 把编译产物包装成 Adapter，`protocol/codec/` 是 schema 定义、
校验、编译与 encode/decode 引擎本体。

## 2. 解析与装配

### 2.1 文件命名与目录约定

每条命名连接一份 codec 配置，放在适配器目录（默认 `conf/adapter/`）：

```
conf/adapter/
├── tcp_logic_codec.json      → 连接 "tcp:logic"
├── tcp_battle_codec.json     → 连接 "tcp:battle"
├── udp_battle_codec.json     → 连接 "udp:battle"
└── errors.json               → 共享错误码描述（全目录一份）
```

文件名规约 `<proto>_<service>_codec.json`（`protocol/codec_resolver.go:33-45`）：

- `<proto>` 指传输协议，即 `tcp` 或 `udp`（不是 protobuf）；
- 去掉 `_codec.json` 后缀后按**首个** `_` 拆分，service 内部允许包含下划线
  （`tcp_rank_team_codec.json` → `tcp:rank_team`，`protocol/codec_resolver.go:70-78`）；
- 拆不出 proto/service、目录不可读、目录下无任何 `*_codec.json` 均返回中文 error，启动期 fail loud
  （`protocol/codec_resolver.go:52-87`）。

### 2.2 CodecResolver

`CodecResolver` 是「server 串 → Adapter」的显式映射，接口只有一个方法（`protocol/codec_resolver.go:89-93`）：

```go
type CodecResolver interface {
    Resolve(server string) Adapter
}
```

- server 串即 `"<proto>:<service>"`（如 `tcp:logic`、`udp:battle`），与 flow.json 中 action 的
  `service` 拼接、listenRef 的 `server` 字段同源；
- **无 fallback**：缺映射返回 nil，由调用方 fail loud（`protocol/codec_resolver.go:97-106`）；
- resolver 构造完成后内部 map 只读，并发 `Resolve` 无需加锁；
- resolver 同时携带每连接的心跳配置，`protocol.Heartbeat(resolver, server)` 取回
  （`protocol/codec_resolver.go:109-114`；Robot 建连时消费，见 `robot/robot.go:1677-1696`）。

### 2.3 加载流程与错误策略

`LoadCodecResolver(codecDir, codecs, errorsFile)`（`protocol/codec_resolver.go:151-248`）：

1. 可选加载共享 `errors.json`（`codec.LoadErrorMap`，扁平 `{"code":"中文描述"}`，key 解析为
   uint64，非数字报错，`protocol/codec/errors.go:10-30`）；
2. **码段契约硬校验**：任一 key `< 100` 立即返回 error——`< 100` 是框架错误码保留段
   （errcode 包），业务码必须 `≥ 100`（`protocol/codec_resolver.go:165-171`）；
3. 按 server 串排序遍历，逐份 `codec.LoadSchema`（JSON Schema 结构校验 + 反序列化 + 领域
   Validate，`protocol/codec/schema.go:253-270`）→ `NewSchemaAdapter`；
4. 同一文件被多个 server 引用时 dedup：编译一次、共享同一个无状态 Adapter 实例
   （`protocol/codec_resolver.go:187-217`）；
5. **帧切割规则一致性校验**：所有 codec 的 `FrameSignature`（headerSize/trailerSize/长度字段
   offset/size/endian/kind 与 includes 口径，`protocol/codec/compile.go:429-456`）必须完全一致，
   不一致在启动期报错——gnet EventServer 只持单一元信息源（`PickMetaAdapter` 取排序首个，
   `protocol/codec_resolver.go:258-278`），异构帧规则会直接错帧，宁可 fail loud
   （`protocol/codec_resolver.go:220-245`）。

空映射、文件缺失、解析失败、校验失败、`errors.json` 加载失败均返回中文 error，绝不静默回退。

### 2.4 errors.json 与 DescribeError

`errors.json` 只放业务码（≥ 100）描述，注入所有 SchemaAdapter；`DescribeError(code)` 未命中
返回空串（`protocol/codec/errors.go:32-38`、`protocol/codec/engine.go:909-914`）。前端
`ErrorMapEditor` 在保存前做同一契约的实时校验，与后端硬报错形成双重防线。

## 3. Adapter 接口（9 方法）

定义于 `protocol/adapter.go:9-53`，`SchemaAdapter` 是唯一生产实现（`protocol/schema_adapter.go:24-29`
编译期断言）。

| 方法 | 语义 | 热路径优化点 |
| ---- | ---- | ---- |
| `HeaderSize() int` | 消息头固定字节数 | 编译产物缓存值，直接返回 int（`protocol/codec/compile.go:423-424`） |
| `BodyLength(header []byte) int` | 从帧头解析 body 长度 | 纯 Go 一次 `readUint` + includes 口径换算，不足 headerSize 返回 0、负值钳 0（`protocol/codec/engine.go:45-60`） |
| `EncodeTCP(route, body, secretKey)` | 编码完整 TCP 帧 | 见 §5.1；单次分配整包 |
| `EncodeUDP(route, body, secretKey)` | 编码完整 UDP 帧 | 与 TCP 共用同一 encode 内部函数（单 transport，偏移已固化在 encrypt 步，`protocol/codec/engine.go:93-100`） |
| `DecodeTCP(data, secretKey)` | 解码 → routeKey + body + headerErr | 管线反序执行；失败原因经 `DecodeTCPWithReason` 打 Warn（`protocol/schema_adapter.go:66-89`） |
| `DecodeUDP(data, secretKey)` | 同上 | 与 `DecodeTCP` 分离，允许 TCP/UDP 不同解码策略 |
| `ExpectedRouteKey(route)` | 从发送路由计算期望响应键 | 栈上 64 字节暂存 + `strconv.AppendInt`，routeKey 驻留表（`protocol/codec/engine.go:66-86`） |
| `Close()` | 释放资源 | SchemaAdapter 为幂等 no-op（`protocol/schema_adapter.go:97-98`） |
| `DescribeError(code)` | 错误码 → 中文描述 | 编译期拷贝 errorMap，只读查表 |

补充两点：

- **route 不透明**：`route any` 逐字传给 codec，引擎不解析其结构。JSON 反序列化出的
  `float64` 由 codec 按 `math.Floor` 取整后写入数值路由字段（`protocol/codec/engine.go:323-355`）。
  `route` 为 nil 视为无路由请求（密钥交换等），路由字段写 0。
- **并发安全**：`SchemaCodec` 构造后无可变状态（`protocol/codec/compile.go:221-232`），任意
  goroutine 并发调用 9 方法无需加锁。

## 4. Schema 全字段参考

根类型 `Schema`（`protocol/codec/schema.go:25-33`）：`version` / `endianDefault` / `frame` /
`header` / `routeKeyTemplate` / `pipeline` / `heartbeat`。

### 4.1 根字段

| 字段 | 类型 | 必填 | 语义 |
| ---- | ---- | ---- | ---- |
| `version` | int | 是 | 必须为 `1`，否则加载期报错（schema.go:301-304） |
| `endianDefault` | string | 是 | `le` 或 `be`，多字节字段的缺省字节序（schema.go:305-307） |
| `frame` | object | 是 | 帧物理布局，见 §4.2 |
| `header` | array | 是 | 消息头字段布局，见 §4.3 |
| `routeKeyTemplate` | string | 是 | 路由键模板，非空；占位符必须指向 role:"route" 字段（schema.go:314-316、475-486） |
| `pipeline` | array | 否 | encode/decode 管线步骤，见 §4.5 |
| `heartbeat` | object | 否 | 连接级心跳配置，见 §4.6 |

### 4.2 frame（FrameSpec，schema.go:37-42）

| 字段 | 类型 | 必填 | 语义 |
| ---- | ---- | ---- | ---- |
| `headerSize` | int | 是 | 帧头字节数，必须 > 0（schema.go:308-310） |
| `trailerSize` | int | 否 | 帧尾字节数，默认 0，不可为负（schema.go:311-313）。帧尾在 encode 时零初始化，decode 时排除在 body 之外（engine.go:621） |
| `lengthIncludesHeader` | bool | 否 | length 字段值是否含 header（encode 写 `body+headerSize`，decode 反算时减去，engine.go:229-238） |
| `lengthIncludesTrailer` | bool | 否 | length 字段值是否含 trailer，同上口径 |

长度字段的**类型与位置不再在 frame 里声明**——由 header 中唯一的 `role:"length"` 字段携带
（schema.go:35-36），`BodyLength` 按该字段的 offset/type/endian 纯 Go 解析
（engine.go:45-60）。

### 4.3 header 字段（Field，schema.go:45-56）

每个元素描述帧头的一个字段：

| 字段 | 类型 | 必填 | 语义 |
| ---- | ---- | ---- | ---- |
| `name` | string | 是 | 字段名，全 header 内唯一（schema.go:327-334） |
| `type` | string | 是 | 见下方取值表（schema.go:211-217） |
| `size` | int | 是 | 字节数，必须 > 0；定宽类型的 size 必须等于宽度，`bytes` 需显式 size>0（schema.go:338-356） |
| `offset` | int | 是 | 帧内偏移，≥ 0 且 `[offset, offset+size)` 落在 headerSize 内（schema.go:335-343） |
| `role` | string | 是 | 七值之一，见下表（schema.go:219-222） |
| `endian` | string | 否 | `le`/`be`，缺省回退 `endianDefault`（schema.go:357-360；compile.go:544-551） |
| `bits` | array | 否 | 仅 `role:"flags"`：命名位列表 `[{name, bit}]`，见 §4.4 |
| `from` | string | 否 | 仅 `role:"checksumOut"`：`"<step>.<output>"`，见 §4.5 produces |
| `source` | object | 否 | 仅 `role:"value"`：取值源，见 §4.4 |
| `repr` | string | 否 | `type:"bytes"` 时的表示法（hex/base64/ascii）。**预留字段：当前引擎无 bytes 字段的解码产出路径，未消费**（schema.go:55） |

字段物理区间两两不重叠（位域共享同一整数不算重叠，schema.go:366-386）。

**type 取值**（schema.go:211-217；宽度和校验见 schema.go:344-356）：

`u8` `u16` `u24` `u32` `u64` `i8` `i16` `i24` `i32` `i64` `f32` `f64` `bytes`

注意：`f32`/`f64` 虽在 schema 类型集合内，但**编译期直接拒绝**——读写层未做 IEEE-754 位
转换，用浮点会静默错帧，等实现补全后再放开（compile.go:538-541）。

**role 七值**（schema.go:219-222）：

| role | 数量约束 | 语义 |
| ---- | ---- | ---- |
| `length` | 有且仅有 1 个（schema.go:409-413） | body 长度字段；encode 写 wire body 长（按 includes 调整），decode 供 `BodyLength` 反算 |
| `route` | 至少 1 个（schema.go:414-416） | 路由字段；encode 从 route map 取值（Floor 取整）写入，decode 读出参与拼 routeKey |
| `errorCode` | — | 协议头错误码；encode 恒写 0，decode 读出作为 headerErr 返回（engine.go:246-248、603-604） |
| `flags` | — | 标志位字段；decode 读出后按各步 flagMask 判定管线步是否执行 |
| `checksumOut` | — | 校验值输出口；值取自 `from` 指向的 pipeline 产物，产物缺失写 0（engine.go:251-261） |
| `value` | — | 常量/路由取值字段；按 `source` 求值（engine.go:262-263） |
| `reserved` | — | 保留位；encode 恒写 0 |

### 4.4 bits 与 source

**bits**（FlagBit，schema.go:59-62）：`role:"flags"` 字段的命名位，如

```json
{ "name": "flags", "offset": 10, "size": 1, "type": "u8", "role": "flags",
  "bits": [ { "name": "encrypted", "bit": 0 }, { "name": "compressed", "bit": 1 } ] }
```

- `bit` 必须在 `[0, size*8)` 内，位号与命名互不重复（schema.go:422-444）；
- pipeline 步通过 `flag` 引用命名位；同一命名位至多被一个 step 绑定（schema.go:596-611）。

**source**（ValueSource，schema.go:67-75）：`role:"value"` 字段的取值源：

| kind | 字段 | 语义 |
| ---- | ---- | ---- |
| `const` | `value` | 固定值 |
| `route` | `key` | 从发送 route map 按 key 取值（Floor 取整） |

`state` / `counter` / `timestamp` 三个 kind 在 Validate 阶段直接报「v1 不支持」（schema.go:244-247、456-469）。

### 4.5 pipeline 步骤（PipelineStep，schema.go:78-90）

| 字段 | 类型 | 必填 | 语义 |
| ---- | ---- | ---- | ---- |
| `op` | string | 是 | `compress` / `encrypt` / `checksum` / `hash` 四值（schema.go:224-226） |
| `name` | string | 是 | 步名，pipeline 内唯一；供 `flag`/`from`/`when.appliesWith` 引用（schema.go:518-527） |
| `algo` | string | 是 | 算法注册表键，编译期校验存在性（compile.go:715-747） |
| `params` | object | 否 | 算法参数，透传给算法实现（如 `{"rol": 3}`，compile.go:693） |
| `flag` | string | 否 | 绑定的命名 flag 位；**带 `when` 的步骤必须绑定 flag**，否则 decode 无法复现 encode 决策（schema.go:613-618） |
| `keyLen` | int | 否 | 仅 encrypt：key 长度要求；`0` 不校验，`>0` 要求 `len(key) ≥ keyLen`（engine.go:286-300） |
| `offset` | object | 否 | 仅 encrypt：`{encode, decode}` 单向偏移，见 §7（schema.go:93-96） |
| `produces` | array | 否 | 派生产物，见下 |
| `over` | object | 否 | 独立 checksum/hash 步的作用域声明，见下 |
| `onError` | string | 否 | `fail`（默认）/ `keep`，见 §5.3 |
| `when` | object | 否 | encode 生效条件，见 §5.4 |

**produces**（StepProduce，schema.go:99-103）：`{name, algo, region}`

- `name`：产物名，步内唯一；`header` 的 `checksumOut` 字段用 `from: "<step>.<produce>"` 引用，
  悬空引用在 Validate/编译期报错（schema.go:621-650；compile.go:567-574）；
- `algo`：计算产物用的算法，先查 checksum 注册表、未命中再查 hash 注册表（compile.go:754-765）；
- `region`：产物计算的作用域，五值 `ciphered` / `bodyPlain` / `bodyFinal` / `header` / `frame`
  （schema.go:228-230）。`ciphered` = 本步**即将处理的明文区域** `body[encOffset:]`（加密前计算，
  engine.go:181-183、373-377）；`bodyPlain` = 管线执行前的原始 body；`bodyFinal` = 该步执行后的
  body；`header`/`frame` 当前按空区域处理（产 0，engine.go:402-404）。

**over**（OverSpec，schema.go:106-110）：`{kind, rangeStart, rangeEnd}`，kind 五值
`bodyPlain`/`bodyFinal`/`header`/`frame`/`range`（schema.go:232-234）。**当前为不可用字段**：
Validate 只查 kind 合法性，但编译层对任何显式声明的 `over` 直接报错拒绝——
`regionForOver` 尚未真正实现，声明了却静默按整个 work 计算会错帧，故加载期 fail loud
（compile.go:697-702）。独立 `checksum`/`hash` 步（无 over）在 encode 对当前 work 计算、
decode 阶段跳过（engine.go:195-221、720-722），同样属未投产的预留能力。

### 4.6 heartbeat（HeartbeatConfigDef，schema.go:130-138）

连接级可选心跳，transport/service 由 codec 文件名对应的连接名决定，不在对象内重复配置：

| 字段 | 类型 | 必填 | 语义 |
| ---- | ---- | ---- | ---- |
| `intervalMs` | int | 是 | 心跳间隔毫秒，必须 > 0（schema.go:505-507） |
| `route` | any | 否 | 心跳包路由（如 `{"cmd": 2, "act": 1}`） |
| `c2sProto` | string | 否 | protobuf body 模式：C2S 消息全名，与 `heartbeatFields` **互斥**（schema.go:508-509） |
| `bindings` | array | 否 | protobuf body 的字段绑定，只能在配置 `c2sProto` 时使用（schema.go:511-512） |
| `heartbeatFields` | array | 否 | raw-binary body 模式：小端字段布局，逐项 `{type, source, ...}` |
| `skipWhenMissing` | bool | 否 | raw 模式 state 源缺失时跳过本 tick 而非报错 |
| `requireSecretKey` | bool | 否 | 等连接设置密钥后再启动心跳 timer |

`heartbeatFields` 的 `source` 取值 `fixed` / `state` / `stateCounter` / `counter` / `timestamp` /
`randomInt`（schema.go:199-207、183-195），配套属性：`value`/`floatValue`（fixed）、`key`
（state/stateCounter）、`start`/`step`（counter）、`min`/`max`（randomInt）、`unit`
（timestamp，`ms`/`s`）；`type` 为小端宽度 `u8/i8/u16/i16/u32/i32/u64/i64/f32/f64`。完整示例见
`conf/adapter/udp_battle_codec.json:25-42` 与 `conf/adapter/tcp_logic_codec.json:25-29`。

## 5. pipeline 语义

### 5.1 encode：按数组顺序执行

`encode`（engine.go:119-272）步骤：

1. 规约 route 为 map，供 guards 与路由字段写入；
2. 按 `pipeline` 数组**正序**执行各步，维护 work（当前 body）/ flags（累计命名位）/
   applied（各步是否生效）/ stash（produces 产物暂存）；
3. 一次性分配 `headerSize + len(work) + trailerSize` 整包（零初始化：未写字节恒 0），写头：
   length 按 includes 口径、route 写 Floor 整数、errorCode 恒 0、flags 累计值、
   checksumOut 取 stash 产物（引用步未执行写 0）、value 按 source、reserved 0
   （engine.go:224-271）；
4. body 拷入头之后返回。

### 5.2 decode：自动反序执行 + 契约 A

`decode`（engine.go:573-729）按 pipeline **逆序**执行——encode「先压后密」，decode 即
「先解密后解压」（engine.go:642-643）。每步是否执行**只看其 flag 位是否在解码 flags 中置位**，
不重算 `when`/guards/minBodyLen/onlySmaller（契约 A，engine.go:646-649）：encode 侧把决策
结果记录进 flag 位，decode 侧只消费 flag，保证两侧决策一致。

- 帧不完整（`len(data) < headerSize+trailerSize`）属正常分包，静默返回 `("", nil, 0)`
  （engine.go:575-578）；
- body = `data[headerSize : len-trailerSize]`（engine.go:621-631）；
- encrypt 步：流密码优先走 `CipherInPlace` 原地解密免整体复制（engine.go:666-683）；
  解密成功后对 produces 做 bcc 校验（重算产物对 region 与头里 checksumOut 比对，
  engine.go:685-690、811-862）；
- compress 步：解压大帧（≥ 1KB 压缩字节）走内容寻址解压去重缓存（engine.go:697-718）；
- 最终按 routeKeyTemplate 用解码出的路由字段值拼 routeKey（engine.go:726-728）。

### 5.3 onError：fail（默认）与 keep

解密失败 / 解压失败 / bcc 校验失败 / 密钥长度不足时（engine.go:657-718）：

- `onError: "fail"`（缺省）：立即返回 `("", nil, headerErr)`，**body 不外泄**；失败原因经
  `DecodeTCPWithReason` 返回，SchemaAdapter 打 `[CODEC] TCP/UDP 解码失败` Warn 日志
  （`protocol/schema_adapter.go:66-89`）；
- `onError: "keep"`：保留当前 work（密文或未解压数据）继续后续步骤。

encode 侧的失败（压缩失败 / 加密失败）不采用该步结果：不置 flag、产物撤回，body 保持原样
（engine.go:153-193）。

### 5.4 when：encode 生效条件（StepCond，schema.go:113-126）

| 字段 | 语义 |
| ---- | ---- |
| `minBodyLen` | body 字节数达到下限才生效 |
| `onlySmaller` | 仅 compress：压缩后严格变小才采用；无法预判，只能在 encode 步内「先压后比」（compile.go:293-297；engine.go:158-167） |
| `requireKey` | 持有非空 key 才生效 |
| `appliesWith` | 依赖另一步已生效（引用步名，schema.go:667-671） |
| `guards` | 路由字段条件数组 `[{field, op, value}]`，op 六值 `eq/neq/gt/gte/lt/lte`（schema.go:122-126、672-677），对 route map 按字段名求值（compile.go:332-341） |

`when` 是 encode-only 决策，decode 不重算（§5.2 契约 A），因此带 `when` 的步必须绑定 flag。

### 5.5 produces 中间产物传递

encode 侧某步执行时把各 produces 的计算值写入 stash，写头阶段由 `checksumOut` 字段按
`from` 引用取值；引用步未执行时写 0（engine.go:251-261）。典型链路：encrypt 步
`produces: [{name: "bcc", algo: "xor8", region: "ciphered"}]` → header `{name: "bcc",
role: "checksumOut", from: "enc.bcc"}`。`ciphered` region 在**加密前**对明文区域计算
（engine.go:181-183），因此 decode 侧解密后可用同区域重算校验（偏移对称时，见 §7）。

## 6. 算法注册表

四张注册表（cipher / compressor / checksum / hasher）在各自实现文件的 `init()` 注册，运行期
只读（`protocol/codec/registry.go:86-133`）。元数据经 `Algorithms()` 导出，供
`GET /sbot/codec/algorithms` 与前端下拉使用（`protocol/codec/registry.go:187-244`）。

### 6.1 cipher（encrypt 步，ciphers.go:569-625）

| algo | 说明 | keyLen | params |
| ---- | ---- | ---- | ---- |
| `none` | 直通（不加密） | — | — |
| `xor` | 循环 key XOR 流密码（自反） | 任意长度 | — |
| `xor_carry_rol` | XOR+carry+ROL8 流密码；key 非 32 字节返回原数据 | 32 字节 | `rol`（int，缺省 3，范围 [1,7] 越界钳位） |
| `rc4` | RC4 流密码（自反）；仅兼容既有协议，禁用于新安全设计 | 1~256 字节 | — |
| `aes_ecb` | AES-ECB + PKCS#7 填充；块密码，密文长度含填充 | 16/24/32 | — |
| `aes_cbc` | AES-CBC + PKCS#7 | 16/24/32 | `iv`（16 字节，必需） |
| `aes_ctr` | AES-CTR 流模式（无填充，len 不变） | 16/24/32 | `iv`（16 字节，必需） |
| `xxtea` | XXTEA 轻量块密码；encrypt 自动补齐 4 字节对齐，key 不足 16 字节报错 | 16 字节 | — |

`none`/`xor`/`xor_carry_rol`/`rc4`/`aes_ctr` 为流语义，实现了 `CipherInPlace` 原地解密扩展
（registry.go:35-46）；块密码（aes_ecb/aes_cbc/xxtea）不实现。

### 6.2 compress（compress 步，compressors.go:157-164）

| algo | 说明 | params |
| ---- | ---- | ---- |
| `none` | 直通 | — |
| `gzip` | RFC 1952 标准库 gzip；压缩阈值由 engine `when` 控制，本层无阈值 | — |

### 6.3 checksum（checksum 步 / produces.algo，checksums.go:77-96）

| algo | 说明 |
| ---- | ---- |
| `none` | 恒 0（不校验） |
| `xor8` | 全字节 XOR（单字节 bcc） |
| `sum8` | 字节累加 mod 256 |
| `crc16` | CRC-16/CCITT（多项式 0x1021，初值 0xFFFF，无反转） |
| `crc32` | CRC-32/IEEE（标准库 `ChecksumIEEE`） |
| `crc32c` | CRC-32/Castagnoli（iSCSI 多项式） |

### 6.4 hash（hash 步，hashes.go:70-89）

| algo | 说明 | params |
| ---- | ---- | ---- |
| `md5` | 16 字节原始摘要；key 非空走 HMAC-MD5 | `key`（可选） |
| `sha1` | 20 字节原始摘要；key 非空走 HMAC-SHA1 | `key`（可选） |
| `sha256` | 32 字节原始摘要；key 非空走 HMAC-SHA256 | `key`（可选） |

## 7. UDP 偏移加密

UDP 场景常用「偏移量部分加密」：帧前 N 字节保持明文，供服务端按明文头部查密钥表路由到
对应连接的密钥，剩余部分加密。

配置在 encrypt 步的 `offset`（单向、每份 codec 固化，schema.go:93-96）：

```json
{ "op": "encrypt", "name": "enc", "algo": "xor_carry_rol", "keyLen": 32, "flag": "encrypted",
  "offset": { "encode": 11, "decode": 0 }, ... }
```

（取自 `conf/adapter/udp_battle_codec.json:20`：发送偏移 11、接收偏移 0。）

- **encode 方向**：前 11 字节明文原样保留，仅加密 `body[11:]`（`Cipher` 接口的 offset 即
  明文前缀长度约定，registry.go:28-33）；
- **decode 方向**：前 0 字节明文，从 `body[0:]` 起解密；
- 两个方向独立配置（单向语义），encode/decode 共用同一管线但偏移各自固化在
  `compiledStep.encOffset/decOffset`（compile.go:728-732）；
- **bcc 校验在偏移不对称时自动跳过**：encode 侧 bcc 对 `body[11:]` 计算、decode 侧重算区域是
  `work[0:]`，两者不是同一段字节，数学上无法比对；engine 在 `encOffset != decOffset` 时跳过
  校验（对称的 TCP 路径正常校验，engine.go:811-821）。

## 8. 配置契约校验与编辑工具

加载期三层校验（`codec.LoadSchema`，schema.go:253-270）：

1. **JSON Schema 结构校验**：`config/validation.ValidateCodec`，内嵌 Draft 2020-12 的
   `codec.schema.json`（santhosh-tekuri/jsonschema/v6，`config/validation/validator.go:33-40、42-79`）；
2. **领域 Validate**：聚合多条中文错误一次性返回（schema.go:290-299），覆盖本节所有
   唯一性/引用/取值域规则；
3. **编译期校验**：算法存在性、`checksumOut.from` 引用解析、flag 掩码解析，以及 §4.3/§4.5
   所列的 `f32/f64` 与 `over` 拒绝（compile.go:474-614、697-702、715-771）。

前端配套（一句话指引，交互细节见前端文档与 `cmd/web/src/components/modules/`）：
`ProtocolConfigEditor` 浮窗 + `codecEditor/`（FrameLayoutEditor / PipelineEditor 等）提供
结构化编辑，`POST /sbot/codec/preview` 调后端真实 codec 引擎跑一次 encode/decode 并返回
帧/字段解释（`protocol/codec/preview.go:44-58`、`admin/httpapi/codec.go`）。

## 9. 新协议接入步骤

1. 新增 `conf/adapter/<proto>_<service>_codec.json`：声明 frame 布局、header 字段
   （role 分配）、`routeKeyTemplate`、pipeline（压缩/加密/校验）与可选 heartbeat；
   注意全目录 codec 的帧切割规则必须一致（§2.3 第 5 条）；
2. flow.json 中 action 的 `service`、listenRef 的 `server` 使用对应 `<proto>:<service>` 串；
3. `.proto` 文件放 proto 目录供 `protox` 动态加载；
4. 服务端错误码描述补充到共享 `conf/adapter/errors.json`（仅 ≥ 100 业务码）。

Go 层零改动：`InferCodecMap` 按文件名规约自动发现，Adapter 接口的抽象保证协议差异完全由
schema 承载。边界：本工具面向原始 TCP/UDP + 自定义二进制协议头 + Protobuf 消息体的游戏
服务器；WebSocket / TLS / 非 Protobuf 消息体不在声明式主路径内。
