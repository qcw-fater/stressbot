# Wire-First 状态平面执行计划

> 2026-07-29 定稿(v3,已并入外部评审意见)。目标:16GB 单机支撑 8000–10000 机器人。
> 本文档是实施期间的唯一准绳;任何偏离必须先改文档再改代码。

## 0. 背景与结论摘要

线上剖面(v0.2.0,5000 人)证实:live 6.4GB 中 ~47% 是**每机器人独有大消息的解码态常驻**
(playerData 等,~600KB/机器人),且已实证任何解码态表示(dynamicpb / Go map / Lua table)
体积同量级(028 vs 026:Frozen 引用比展开 map 还差 +217MB)。唯一小一个数量级的形态是
wire 字节本身(实测某环境 6KB,老号估 40–120KB)。同时 `HeapObjects = 1.25 亿`(2.5 万/机器人)
是 GC CPU 与尾延迟的隐性成本。

**主路线:状态平面字节化(wire-first)。** 整存消息的权威形态是服务器原始字节快照,
读取走 wire 扫描惰性解码,写入走 overlay 覆盖层。每机器人状态常驻收敛到
"Σwire + 被写子树 + 小 overlay",与服务器消息形态解耦。

## 1. 硬约束

- 业务脚本逻辑零改动(存哪些数据不可变);**唯一例外**:`request_player_data.lua` 的
  `set(get_field_map(resp))` → `set(resp)`(同一份完整数据,仅换 API,属"用法可改")。
- 不以 `GOMEMLIMIT` 等环境变量作为架构方案。
- 表示选择的启发式参数用**相对比例**;**资源安全上限必须绝对有界**
  (缓存字节、overlay 规模、scratch 池、队列容量都要硬顶)。

## 2. 架构不变量

1. **字节即真相**:进入 state 的整存值,权威形态是服务器原始字节的独立快照;
   一切解码产物(Lua table、overlay、投影)都是派生视图,可错、可重建,真相不可污染。
2. **读路径承担全部新逻辑,存储侧零转换**:扫描器 bug 只能表现为"读错",
   可修复、可重读、可在线校验;不存在把 bug 固化进存储的路径。
3. **state 表示封闭**:map/list/WireValue 的类型分支只允许存在于 `state/` 与 `protox/`
   内部;engine/script 消费方一律通过 Store 的操作接口访问。
4. **语义等价以 dynamicpb 反射导航为 oracle**(即现行 messageToMap 契约),
   由差分 fuzz(离线)+ 影子验证(在线)共同证明;decoded 后端永久保留作 oracle 与降级目标。

## 3. 组件设计

### C1 `protox.WireValue`

```go
type WireValue struct {
    schema   protoreflect.MessageDescriptor
    digest   uint64   // schemaID:descriptor 全名+字段布局摘要
    raw      []byte   // ownedRaw:独立快照(网络缓冲只拷贝一次)或父 blob 子切片
    // 子消息字段多 occurrence 时的规范化缓存等内部字段
}
```

- 单 occurrence 的 singular message 字段 → 父 raw 的**零拷贝子切片**;
- 多 occurrence → **拼接规范化**(protobuf 性质:字节拼接 = 消息合并,递归成立),
  拼接只在首次遇到时做一次;
- **结构校验器**:凡跳过 `Factory.Parse` 直接入 state 的字节(Go-store/引擎整存),
  必须先过零分配 tag/len 骨架校验——收包时结构非法立即报错,与今日 Parse 失败时机一致,
  不允许把失败推迟到读取时。

### C2 `PathProgram`

路径字符串 → 字段号访问程序,按 `(schemaDigest, path)` 全局缓存(路径集合由配置固定、有界)。
程序含:字段号序列、kind、packed 兼容标记、map key 类型、`[N]` 索引段。
编译期校验路径合法性(字段不存在即报错,不留到运行时)。

### C3 wire 语义矩阵(正确性契约,差分 fuzz 逐项覆盖)

| 场景 | 语义 |
|---|---|
| scalar/enum 同字段多次出现 | last-wins |
| oneof | 跨成员最后出现者生效(后者清前者) |
| singular message 多次出现 | **合并**(实现 = 字节拼接) |
| repeated | 保序;packed/unpacked 混排按出现顺序合并 |
| map 重复 key | 最后 entry 生效;entry 内 key=1/value=2,缺省用默认值 |
| proto3 隐式标量 wire 缺失 | 读出类型默认值(在场)——与 messageToMap 一致 |
| proto3 message 字段 wire 缺失 | not found——与 messageToMap 跳过规则一致 |
| proto3 `optional`(synthetic oneof) | presence 依 wire 出现 |
| 全 kind 解码 | varint/zigzag(sint)/fixed32/fixed64/float/double/bytes/string/enum/bool |
| 非法 wire type / 越界长度 / 截断 | 读取返回错误;入口结构校验拦截 |
| int64 → Lua | 精度规则复用现行 `fromScalarValue` 终端转换 |

### C4 OverlayTrie(写模型,取代 WireValue 上的 ShallowMap COW)

- 节点 = `{wire 引用, overrides map[string]any}`,overrides 支持**墓碑**
  (`set_path(x, nil)` 必须遮蔽 wire 旧值——`listen_guild_kick_member.lua` 实际用到);
- 读优先级:**overlay > wire > proto3 默认值**;
- `SetPath` 只创建路径书脊上的 overlay 节点,**绝不物化未写兄弟字段、不产生默认值对象**;
- overlay 总规模计入每机器人硬上界(超限告警,防脚本写爆)。

### C5 state API 封闭

Store 对外收敛为操作接口,表示分支内部化:

- `Lookup(path) any` — 终端取值(解码标量/临时子视图)
- `Iterate(path, fn)` — 列表/映射遍历(wire 单遍流式)
- `Pick(path, strategy)` — 随机选取(蓄水池采样,消灭 `[N]` 循环 O(n²))
- `WithPath(path, value)` — 写(overlay)
- `Materialize(path) any` — 显式物化为普通容器(供必须拿实体的少数场景)

改造清单(M2 逐个归位):`engine/action.go` 的 GetList/GetMap 直接类型依赖、
binding 解析(`stateRandom` 等 17 种)、filter(`CompareValues` 输入)、
Lua `robot.get/get_path/get_list/get_map`。

### C6 响应包装与所有权(dirty 契约)

Lua 响应 userdata 扩为 `{msg(decoded), ownedRaw []byte, schemaID, dirty bool}`:

- W1a 阶段解码照旧发生(`resp` 行为与今日逐字一致),仅随附 ownedRaw;
- `proto.set_field` → `dirty = true`(现有脚本审计:set_field 只用于 `proto.create`
  的新消息,响应从未被改写;契约仍须覆盖);
- `robot.set(msg)`:`dirty || ownedRaw == nil` → `proto.Marshal` 重序列化;否则复用 ownedRaw;
- 网络缓冲(gnet 复用底层数组)→ 入 WireValue 前**恰好一次**拷贝。

### C7 三层验证体系(取代"1% 随机采样")

- **L1 差分 fuzz(离线,M0/M1)**:随机 schema + 随机消息 + 恶意变体
  (乱序 tag、重复 singular 字段、packed/unpacked 混排、多 occurrence 子消息、截断),
  wire 导航 vs dynamicpb 反射导航逐路径比对,语义矩阵逐行建 case;
- **L2 首现必双读(在线,确定性覆盖)**:每 `(schemaDigest, pathProgram)` 首 K 次
  (K=3)读取强制双读比对——覆盖全部实际使用路径,成本一次性;
- **L3 指纹采样(在线,持续)**:按 wire shape 指纹(字段集合+体积桶)补充低采样;
- **mismatch 处置**:以权威侧(decoded)结果返回、该 schema 降级 decoded 后端、告警;
- **稳定切换条件**:全部已编译 PathProgram 通过 L2 且 L3 零 mismatch,
  而非"运行足够长时间"。

### C8 单步投影(CPU 模型:K×bytes → ~bytes)

executor 单步(一次 action 的 bindings+conditions+store mappings)开始时,
将本步所需路径合并编译为 projection trie,同一 WireValue **单遍扫描多路提取**;
scratch 自池租借、步末清空、不跨写操作——无缓存失效问题。
持久 memo 明确**不做**(失效正确性风险),实测顶不住再单点评审。

### C9 去重缓存 wire-only

`FrozenCache` 条目改存字节:入缓存零解码(hash+memcmp 本就在字节上),
命中返回共享 WireValue;`estimateDecodedCost` 及其常量删除;容量按字节硬上界。

### C10 批量 span 保留规划器(路径式存储)

一次响应的全部 store mapping 先收集终端 span 集,再整体决策:
`保留整 blob` vs `拷贝全部唯一 span(重叠先合并)`,取实际常驻更小者;
整消息同时被整存则所有子视图直接共享原 blob。**没有 1/4 之类的任意常量。**

## 4. 阶段计划与出口门禁

### M0 — 契约与骨架(先于一切实现)

| 任务 | 产出 |
|---|---|
| 本文档评审定稿 | 契约冻结 |
| Σwire 常驻消息审计:全部整存点(引擎 `Field==""`、Go-store、脚本 `robot.set(resp)`)与路径式存储点,列出消息类型与预估体积 | 审计表,容量模型输入 |
| 差分 fuzz 骨架:`protox/wiretest`,随机 schema 生成器 + 消息变异器 + 双导航比对器(先 dynamicpb 对 dynamicpb 自检跑通) | fuzz harness 合入 |
| `PathProgram` 数据结构与编译器规范 | 与 C2 一致的接口定义 |

**出口**:文档定稿;fuzz 骨架自检绿。

### M1 — 只读 WireValue

| 任务 | 涉及 |
|---|---|
| `WireValue` + 扫描器 + 结构校验器 + 多 occurrence 拼接规范化 | `protox/wirevalue.go`(新) |
| `PathProgram` 编译器 + 全局缓存 | `protox/pathprogram.go`(新) |
| 差分 fuzz 全绿(语义矩阵逐行 + 变体) | `protox/wiretest` |
| state `PathNavigator` 适配(只读接入) | `state/store.go` |
| 接线只读整存点:引擎 `storeResponseProto` 整存、`robot.go` Go-store 整存 → WireValue(替代解码留存) | `engine/action.go`、`robot/robot.go` |
| L2/L3 影子验证框架 + `/debug/wire` 观测端点(每 key 存储形态与字节数) | `protox/`、`state/` |
| 单步投影 trie(可顺延至 M2 初) | `engine/` |

**出口**:fuzz 全绿;5000 人基线影子零 mismatch;整存部分 live 显著下降。

### M2 — 写模型与 state 封闭

| 任务 | 涉及 |
|---|---|
| OverlayTrie(含墓碑)+ `SetPath` 重构 | `state/store.go` |
| state 集合 API 封闭(C5)+ 消费方逐个归位 | `engine/action.go`、`script/api_robot.go` |
| 响应包装 ownedRaw/dirty(C6)+ `luaToGoStoreValue` → WireValue | `script/api_network.go`、`api_proto.go`、`api_robot.go` |
| `request_player_data.lua` 一行用法改动 | `conf/scripts/` |
| `goValueToLua` / `robot.get` 支持 WireValue | `script/api_robot.go` |

**出口**:全部已编译 PathProgram 完成 L2 影子验证;5000 人 live 预期 ~3.5–4GB。

### M3 — 广播 wire-only

`protox/dedup.go` 改造(C9)。**出口**:命中率 ≥ 80%(条数 4096 修复已部署),缓存字节硬上界生效。

### M4 — 批量 span 规划器

替换 `GetFieldForStore` 的 messageToMap 展开(C10)。
**出口**:messageToMap 常驻归零,路径存储增长尾巴消失。

### M5 — 容量门禁

5000 → 8000 → 10000 分阶段压测。**验收(双上限)**:

- `N_memory`:RSS p99 ≤ 12GiB;
- `N_cpu`:稳态 CPU ≤ 80%;
- 无 mailbox drop;影子零 mismatch;增长率收敛(≤ 1MB/min 且趋零)。
- 最终容量 = min(N_memory, N_cpu)。

## 5. 容量模型(区间制,与具体服务器解耦)

每机器人边际 = Σwire(6–100KB,取决于账号态)+ overlay/写子树(0–150KB)
+ 脚本自写状态(~20KB,028 剖面实证:playerData 之外 luaToGoValue 留存 < 100MB@5000)
+ Lua VM(240KB,本计划不动)+ 网络/栈(~120KB)≈ **390–630KB**。

- 内存:live @10k ≈ 4.4–6.8GB(+共享 ~0.5GB)→ RSS ≈ 10–14GB,区间上界处紧,
  由 churn 下降(整存不再全量解码、GC 对象数坍缩两个数量级)对冲;
- CPU:5000 人现值 ~72–76% 主机占用,10k 大概率**先撞 CPU**;对冲项:解码总量下降、
  投影单遍扫描、GC CPU 下降(HeapObjects 坍缩)。M5 实测定论,超限即横向加节点。

## 6. 风险登记

| 风险 | 等级 | 对策 |
|---|---|---|
| wire 扫描语义 bug → 静默读错状态 | **最高** | C7 三层验证;decoded 后端永久保留;mismatch 自动降级 |
| 极端大消息 × 高读频 → CPU 放大 | 中 | C8 投影;工具文档标注该特征;实测后单点评审 memo |
| state 形态复杂度扩散 | 中 | 不变量 3 + C5 封闭;code review 检查点 |
| overlay 被脚本写爆 | 低 | 每机器人 overlay 字节硬上界+告警 |
| 灰度期双读导致基准失真 | 低 | 灰度期数据不作为基准,流程标注 |
| `robot.set(展开表)` 反模式回归 | 低 | AGENTS.md/脚本规范约定:大消息整存传句柄不传展开表 |

## 7. 已证伪方案存档(勿重试)

- playerData 存 decoded `Frozen` 引用:+217MB @5000(028 vs 025 同相位),已还原;
- 按访问面裁剪留存字段:违反"业务脚本逻辑不可改"约束,已还原;
- 扁平 arena 自造格式:正确性结构劣于 wire(转换 bug 固化进存储)、内存 2–3× wire;
- 去重缓存按原始字节设上界:钉住 ~50× 解码树(020–022 实证);M3 wire-only 后此问题消失。
