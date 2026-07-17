---
name: update-readme
description: Use when 更新或生成 stressbot 项目 README.md，校对代码与文档一致性、发现过时章节、补充 FlowEditor/onError/listens/声明式 codec/Admin/Agent/监控等说明，或用户说“更新 README”“生成文档”“README 过时了”时。
---

# stressbot README 更新技能

## 核心原则

1. **准确 > 完整 > 简洁** — 每个技术细节必须与代码一致
2. **先审查再动手** — 不假设架构不变，每次都从代码重新核对
3. **按功能模块组织** — 每个模块前后端一体描述，不拆开

---

## 一、总体流程

```
代码审查 → 差异分析 → 用户确认 → 分区执行 → 全局校验
```

### 1.1 代码审查（不可跳过）

**每次更新前必须重新读取代码**，即使上次刚更新过。项目持续演进，以下内容可能已变更：

```
□ engine/flow.go         — 新增/删除 pattern 常量、binding type、struct 字段
□ engine/action.go       — 新增 pattern 执行逻辑、resolveFieldValue 新 case
□ engine/executor.go     — 新增节点类型
□ adapter/adapter.go     — 接口方法变更
□ script/api_*.go        — Lua 函数增删、签名变更
□ cmd/agent/main.go      — Config struct 新增字段
□ admin/handlers.go      — HTTP 端点增删
□ admin/config.go        — Admin 配置字段变更
□ agent/config.go        — Agent 配置字段变更
□ monitor/collector.go   — 指标类型变更
□ state/store.go         — CompareValues 新增运算符
□ network/connection.go  — 请求-响应/监听机制变更
□ robot/manager.go       — 启动策略变更（ramp-up 等）
□ cmd/web/package.json   — 前端依赖变更
□ cmd/web/src/types/     — TypeScript 类型与 Go struct 对应关系变更
```

审查方法：对每个文件执行 `grep` 提取关键常量/函数/字段，与 README 对应表格逐项比对。

### 1.2 差异分析 → 用户确认 → 分区执行 → 全局校验

---

## 二、输出结构（目标 README 目录）

README 按功能模块组织，**每个模块前后端一体描述**。以下为推荐结构，实际更新时根据审查结果增删调整：

```markdown
# stressbot — 通用游戏服务器压测工具

项目简介（一段话 + 核心设计原则）

---

## 目录结构

## 快速开始
### 单机模式
### Agent 模式
### Admin 模式

## 架构概览
### 分层依赖图
### 单次动作数据流（从 Executor 到 gnet 的完整链路）
### 分布式架构总览

---

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# 第一部分：流程设计与执行
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#
# 全栈功能：前端可视化编辑器 + 后端引擎执行
# 共享数据模型：flow.json（TaskFlow / Node / ActionDef / ListenDef）
# 前端：FlowEditor（画布 + 节点面板 + 编辑抽屉 + Proto 浏览器 + 校验）
# 后端：engine/（Executor 遍历节点图 + ActionExecutor 执行动作）
#

## flow.json 总体结构
（defaultDelayMs / nodes / actions / listens 四个顶层对象）

## 可视化编辑器
### 画布与节点面板（拖拽创建节点，8 种节点类型）
### 节点编辑抽屉（action 编辑：route / proto / bindings / store / delay）
### Proto 浏览器（浏览已加载的 proto 消息类型和字段，用于动作编辑）
### 校验报告（实时检查 flow.json 引用完整性和语义错误）
### 模板库（可复用的 action/listen 模板）
### 撤销重做（Ctrl+Z / Ctrl+Shift+Z）
### 编辑稿自动保存（localStorage 持久化）

## 节点类型
### 控制流节点
- sequence — 顺序执行子节点列表
- loop — 循环（支持无限、前置条件、后置条件、break/continue）
- boolean — 条件分支（trueNext / falseNext）
- weighted — 加权随机（options: [{node, weight}]）
- wait — 显式等待
- break / continue — 循环控制（break 跳出 / continue 跳过本次）

### 执行节点
- action — 执行声明式动作（引用 actions 表）或通过 listenRefs 注册持久化推送监听

### 各类型 JSON 格式 + 完整示例

### 节点通用字段表
（type / next / body / loopCount / condition / breakCondition / trueNext / falseNext /
 options / action / onError / listenRefs / waitMs / waitMin / waitMax / delayMs）

### 条件表达式
（state:key op value — 内置比较 + lua:script.lua — 脚本求值）

## actions — 声明式动作

### pattern 一览
（全部常量 — 从 engine/flow.go 读取。当前 16 个：
 tcpSend / tcpRequest / tcpConnect / tcpClose / tcpListen /
 udpSend / udpRequest / udpConnect / udpClose / udpListen /
 tcpHeartbeat / udpHeartbeat / httpRequest / setState / clearState / lua）

### 请求-响应类
（tcpRequest / udpRequest — channel 一发一收 + 超时）
（tcpListen / udpListen — 轮询等待推送 + 超时 + pollMs）

### 连接管理类
（tcpConnect / udpConnect / tcpClose / udpClose）

### 发送类
（tcpSend / udpSend / httpRequest — 支持 JSON/form body）

### 状态、心跳与辅助类
（tcpHeartbeat / udpHeartbeat / setState / clearState / lua）

### ActionDef 全字段表
（pattern / service / route / script / address / c2sProto / s2cProto /
 bindings / store / timeout / pollMs / keys / url / method / contentType / intervalMs / heartbeatFields / skipWhenMissing；optional 属于 FieldBind，不属于 ActionDef）

## bindings — 字段绑定

### binding type 一览
（从 engine/flow.go + engine/action.go 读取。当前 17 个：
 fixed / state / stateFirst / stateRandom / stateRandomN /
 stateMapKey / stateMapValue / randomPick / randomPickN / randomPickMap /
 randomExclude / randomInt / randomFloat / randomBool / randomString / listSize / map）

### 取值类（fixed / state / stateFirst）
### 随机类（stateRandom* / stateMap* / randomPick* / randomExclude / randomInt / randomFloat / randomBool / randomString）
### 辅助类（listSize / map）
### 通用属性（optional / required / wrap / storeAs / path / filters）
### 条件绑定（ConditionDef — source / path / op / value / valueSource）

## store — 响应字段存储
（StoreMapping：field（含嵌套路径） + setter）

## listens — 持久化监听定义

### 声明式回调（s2cProto + store 自动解析存储）
### Lua 回调（script 指定脚本，接收 onMessage(r, msg)）
### ListenRef 注册（action 节点 listenRefs: route / server / listen / queueSize；listen 引用 listens 表，空 = 仅轮询不回调）

## filters — 过滤器
（全部运算符 — 从 state/store.go CompareValues 读取。当前 10 种：
 eq / neq / gt / gte / lt / lte / contains / in / notNil / isNil）

## 心跳
### 声明式（tcpHeartbeat / udpHeartbeat，proto / raw-binary / 空 body，Go-only builder）
### Lua（复杂行为保留在脚本内；优先使用声明式心跳）

---

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# 第二部分：协议适配
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#
# 全栈功能：前端适配器编辑器（ResourcesDrawer Adapter tab）+ 后端声明式 codec（CodecResolver / SchemaAdapter + codec/ Go 引擎）
# 共享文件：conf/adapter/<proto>_<service>_codec.json + conf/adapter/errors.json；codec.lua/error.lua 仅保留为 T1 一致性测试 oracle，非生产路径
#

## 适配器架构
### 热路径优化（HeaderSize / BodyLength 纯 Go 缓存，帧解析零 Lua 调用）
### 编解码路径（EncodeTCP / EncodeUDP / DecodeTCP / DecodeUDP 通过 SchemaAdapter 委托 codec.SchemaCodec）
### Adapter 接口全方法（从 adapter/adapter.go 读取，当前 9 个，含 DescribeError）

## 声明式 codec.json 要求
### frame / header / route / encrypt / errorCode 等结构
### 每连接一份 <proto>_<service>_codec.json，共享 errors.json
### UDP 加密 offset 单向配置（encode/decode 可不同）

## 声明式 codec.json 模板（完整示例）

## 适配器编辑器
（前端 ResourcesDrawer Adapter tab：内嵌 Monaco 编辑器 + 导入文件 / 载入模板 / 保存 /
 接口规范说明 / 7 函数校验 + 错误提示）

---

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# 第三部分：Lua API 参考
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#
# 前端：脚本文件在 ResourcesDrawer Lua tab 管理
# 后端：script/ 包实现，6 个模块当前共 63 个函数
#

## 脚本执行模式
（RunActionScript: execute(r) → code；WireBytes 由 Context 自动统计）
（RunBooleanScript: execute(r) → boolean）
（RunCallbackScript: onMessage(r, msg)）

## network 模块
### 连接管理（connect_tcp / connect_udp / close_tcp / close_udp）
### 请求-响应（tcp_request / udp_request — 参数、返回值、超时默认值）
### 单向发送（tcp_send / udp_send）
### 监听轮询（tcp_listen / udp_listen — 超时、pollMs）
### 加密密钥（set_tcp_secret_key / get_tcp_secret_key / set_udp_secret_key / get_udp_secret_key）
### 监听占位（ensure_tcp_listener / ensure_udp_listener）
### HTTP（http_request — method / contentType / body table）

## robot 模块
### 读写（get / set / has / delete / clear / increment / keys）
### 高级（get_path / get_id / get_account / get_context）

## proto 模块
### 构建（create / set_field / serialize）
### 解析（parse / get_field / get_field_map）
### 列表（iter_list / list_size / list_get）
### 对象方法语法（userdata __index）

## utils 模块
### 随机（random_int / rand_range / random_bool / random_string / random_pick / random_pick_n / weighted_pick / rand_filter / rand_filter_one / shuffle）
### 二进制（pack_le / unpack_le — u8/i8/u16/i16/u32/i32/u64/i64/f32/f64）
### 工具（sleep / time_ms / fnv_hash）

## json 模块（encode / decode）
## log 模块（debug / info / warn / error）

## Go-Lua 类型转换规则
（int64/uint64 超 2^53 以字符串返回 / table 转换 / userdata proto message）

---

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# 第四部分：运行与配置
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#
# 全栈功能：前端 TaskStartModal + RuntimeBar + 后端 cmd/agent + admin/task
# 共享数据模型：config.json + RobotConfig + RampUpStage
#

## 运行模式
### 单机模式（agent.enabled=false，直接运行完整启动序列）
### Agent 模式（agent.enabled=true，注册到 Admin，接收任务）

## 任务生命周期（全栈）
### 创建（前端 TaskStartModal：填写名称/机器人数/并发/加压/日志级别 + 收集资源）
### 启动（分配 Agent + 下发配置 + 各 Agent 启动机器人）
### 运行中（前端 RuntimeBar：状态 badge + 停止按钮 + 只读画布）
### 停止（优雅关闭：等待 Agent 完成 → 归档历史）
### 状态机（pending → starting → running → stopping → stopped / failed）

## config.json 完整字段
（从 cmd/agent/main.go Config struct 读取全部 json tag）

### bot 配置（accountPrefix / startNumber / count / concurrentNum / mainService）
### 启动策略
- 批量启动（StartAll — 全量 + 限速）
- 渐进加压（StartWithRampUp — stages: count / concurrency / holdSec）
### 网络配置（heartbeatInterval / tcpTimeout / httpTimeout）
### 协议配置（adapter.script / adapter.poolSize）
### 日志配置（path / level / printConsole / maxSize / maxBackups / maxAge / compress）
### 监控配置（enabled / reportInterval / httpEnabled / httpPort / csvPath / apdexT）
### Agent 配置（adminAddr / name / listenAddr / maxBots / 各 interval / 心跳失败退出）
### 初始状态（stateExtra — map[string]string，注入所有机器人 state）

## admin-config.json 完整字段
（从 admin/config.go Config struct 读取）

---

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# 第五部分：资源管理
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#
# 全栈功能：前端 ResourcesDrawer + BaselineSyncModal + 后端 baseline API
# 共享文件：conf/proto/* + conf/scripts/* + conf/adapter/<proto>_<service>_codec.json + conf/adapter/errors.json
#

## 资源类型
### Proto 文件（conf/proto/ — 动态加载，全名/短名查找）
### Lua 脚本（conf/scripts/ — 启动时预编译）
### 协议适配器（conf/adapter/<proto>_<service>_codec.json，每连接一份；共享 errors.json；codec.lua/error.lua 仅为 T1 oracle）

## 资源编辑器（前端 ResourcesDrawer）
### Proto tab（上传/编辑/删除/清空 + Monaco 编辑器）
### Lua tab（同上）
### Adapter tab（内嵌 Monaco + 模板/导入/保存/校验/接口规范）

## 基线同步
### 同步机制（打开编辑器/启动任务时自动对比远端 vs 本地 IDB 内容）
### 冲突检测（BaselineSyncModal — Monaco DiffEditor 逐个确认保留本地/采用远端）
### 离线跳过（用户确认保留本地的冲突记录，下次不再提示）

## 任务下发流程
（taskActions.startTask → 收集 IDB 全部资源 → multipart POST → Admin 持久化 → 下发 Agent）

---

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# 第六部分：网络与状态
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#
# 框架内部机制，高级用户和开发者需要了解
# 后端：network/ + robot/ + state/
#

## 网络架构
### 连接管理（Client — 命名连接池，TCP/UDP 按 service 名管理）
### 请求-响应机制（responseMap + buffered channel + 超时 select 三路）
### 持久化监听（listenResp + listenCh + listenLoop goroutine + 回调分发）
### 帧解析流程（gnet OnTraffic: peek header → BodyLength → read frame → Decode → dispatch）
### 数据发送（Connection.sendFunc 注入，gconn.AsyncWrite）
### 带宽追踪（Connection.Send 调用 monitor.AddBandwidth）
### 连接生命周期（onDisconnect 意外断开触发 / onClosed 所有关闭触发）
### 多服务支持（mainService 断开自动停止机器人，防止僵尸连接）

## 状态管理
### Store 操作
（Get / Set / GetInt / GetInt32 / GetInt64 / GetString / GetList / GetMap /
 Increment / IncrementInt64 / Delete / Clear / Has / Keys）

### 路径导航（SplitPath — "a.b[0].c" 逐层取值）
### CompareValues 运算符（10 种完整列表 + 语义）
### 类型转换（ToInt64 / ToFloat64 / ToFloat64Safe / DeepEqual）

## 机器人管理
### Robot 生命周期（创建 → Start → goroutine → Stop → Close 释放资源）
### Manager 批量启动（StartAll 全量 / StartWithRampUp 分阶段）
### 限速策略（每 concurrentNum 个暂停 1 秒，防连接风暴）
### 优雅关闭（CloseAll 等 listenLoop → 并行 Close + Wait）

## 心跳机制
（per-connection，独立 goroutine，builder 构建完整包，重注册安全）

## 条件解析器
（递归下降：or → and → unary → atom → comparison，支持括号嵌套）

---

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# 第七部分：分布式架构
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#
# 全栈功能：前端 AgentsPanel + 后端 admin/ + agent/
# 共享数据模型：AgentBrief / TaskAssignment / TaskCompletionReport
#

## Admin 服务器
### 核心组件（TaskStore / AgentRegistry / MetricsAggregator / AgentDispatcher / Assigner / HistoryStore / Sampler）
### HTTP API（全部端点 — 从 admin/handlers.go + handlers_history.go 读取）
  - Agent 上行（register / heartbeat / deregister / stress / system / task-done）
  - 任务 CRUD（create / list / get / start / stop / delete / config download）
  - Agent 管理（list / get / delete / shutdown / shutdown-all）
  - 指标（metrics / metrics/agents / system / system/agents）
  - 历史（list / tags / get / update / delete / clone / compare / timeseries / config）
  - 日志（admin logs / agent logs / log files）
  - 资源基线（resources/baseline）
### 任务生命周期（状态机 + 单例约束）
### Agent 管理（注册 / 心跳 / 健康检查 unhealthy→offline / 离线自动清理）
### 分配策略（proportional 比例 / debug-single 调试单机）
### 前端 Agent 管理面板（AgentsPanel — 状态/CPU/MEM/容量）

## Agent 节点
### 生命周期（注册指数退避 / 心跳循环 / 任务轮询 / 优雅退出）
### 任务执行（TaskRunner: 下载配置 → 加载适配器 → 编译 proto → 构建流程 → Manager → 启动机器人）
### 指标上报（StressReporter 任务期间 / SystemReporter 始终运行）
### 本地 HTTP API（task / stop / shutdown / version / status / logs）

## 集群部署
### 部署拓扑（Admin :8080 ← 多 Agent :7070 → 目标服务器）
### MySQL 历史归档（6 表 / 采样 / 裁剪 / 克隆 / P99 对比）
### 前端历史面板（HistoryModal — 搜索/标签/详情/对比/克隆重跑）

---

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# 第八部分：监控与日志
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#
# 全栈功能：前端 MonitorDock + SystemTab + LogsTab + 后端 monitor/ + logview/
# 共享数据模型：StressSnapshot / ClusterSystemSnapshot / LogQueryResult
#

## 指标采集
### 原子计数器（热路径零锁：成功/失败/超时/跳过/执行中/字节数/错误分布）
### 延迟直方图（16 固定桶 0~60s+，P50/P90/P95/P99）
### Apdex 评分（satisfied / tolerating / 阈值 T 可配）

## 分布式聚合
（MergeSnapshots — 跨 Agent 计数求和 + 直方图合并 + 百分位重算 + 错误合并）

## 前端监控面板
### 实时指标（MonitorDock — 动作表：成功率/延迟/Apdex/QPS/错误分布）
### 系统资源（SystemTab — CPU/MEM 仪表盘 / goroutine / 网络）
### 趋势图（TrendsTab — 时序变化）

## 导出
（Console Reporter / HTTP JSON / CSV / pprof）

## 日志系统
### 结构化日志（zap + lumberjack 轮转）
### 实时日志查看（logview 环形缓冲区 — cursor 查询 API）
### 前端日志面板（LogsTab — Monaco 日志查看器 + 轮询 + 文件下载）
### 告警（企业微信 webhook — DPanic 级别自动推送）

---

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# 第九部分：Web 前端
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#
# 仅前端技术栈和开发指南（各功能的前端描述已在对应章节中）
#

## 技术栈
（React 18 / Vite 5 / Ant Design 5 / React Flow 12 / Monaco Editor / Zustand / ECharts / idb-keyval / protobufjs）

## 开发与构建
### 开发（npm run dev — :5173 + conf/ 挂载 + /api 代理到 Admin）
### 构建（npm run build → dist/，Admin 静态托管）
### 测试（npm run test — Vitest）

## 主题与配置
（暗色/亮色切换 / antd 中文 locale / Vite confMountPlugin）

---

## 调试与 Tips
```

### 结构设计原则

1. **功能模块前后端一体** — 每个模块包含：概念 → 配置语法 → UI 操作 → 内部机制
2. **用户路径驱动** — 设计流程 → 适配协议 → 写脚本 → 配置运行 → 观测结果
3. **"运行与配置"集中** — 用户最关心的运行参数、启动策略、任务生命周期在一处
4. **"资源管理"独立** — Proto/Lua/Adapter 的上传/编辑/同步/下发全链路
5. **"网络与状态"独立** — 框架内部机制，高级用户/开发者参考
6. **Web 前端只保留技术栈** — 各功能的前端描述已在对应章节中，避免重复
7. **功能可增删** — 审查发现新模块时在对应位置插入

---

## 三、关键比对点

以下为已知审查要点。**每次更新时先 grep 确认是否仍正确**，有变化以代码为准。

| # | 比对项 | 文件 | grep 命令 |
|---|--------|------|----------|
| 1 | action pattern | engine/flow.go | `grep -n 'Pattern\w\+\s*=' engine/flow.go` |
| 2 | binding type | engine/action.go | `grep -n 'case "' engine/action.go \| head -30` |
| 3 | 节点类型 | engine/executor.go | `grep -n 'case "sequence"\|case "action"\|case "loop"\|case "boolean"\|case "weighted"\|case "wait"\|case "break"\|case "continue"' engine/executor.go` |
| 4 | 适配器方法 | adapter/adapter.go | `grep -E '^\s+\w+\(' adapter/adapter.go` |
| 5 | Lua 函数 | script/api_*.go | `grep -c 'L.Push.*lua.LFunction' script/api_*.go` |
| 6 | 配置字段 | cmd/agent/main.go | `sed -n '/type Config struct/,/^}/p' cmd/agent/main.go` |
| 7 | Admin 配置 | admin/config.go | `sed -n '/type Config struct/,/^}/p' admin/config.go` |
| 8 | 过滤器运算符 | state/store.go | `grep -n 'case "eq\|case "neq\|case "gt\|case "contains\|case "in"\|case "time\|case "daily\|case "notNil\|case "isNil' state/store.go` |
| 9 | HTTP 端点 | admin/handlers.go | `grep -n 'mux\.\(HandleFunc\|Get\|Post\|Put\|Delete\)' admin/handlers.go` |
| 10 | 前端依赖 | cmd/web/package.json | `grep -A20 '"dependencies"' cmd/web/package.json` |
| 11 | TS 类型对应 | cmd/web/src/types/ | 确认 TypeScript interface 与 Go struct json tag 一致 |

---

## 四、执行策略

### 4.1 分批并行方案

**第一批：代码审查（可并行，只读）**
- Subagent A：engine/ 全部 pattern + binding + node type
- Subagent B：script/ 全部 Lua 函数签名
- Subagent C：cmd/agent/ + admin/ 配置字段 + HTTP 端点
- Subagent D：adapter/ + monitor/ + network/ + robot/ + state/

**第二批：差异报告**（主 agent 合并，输出给用户确认）

**第三批：章节更新（可并行，按功能模块分）**
- Subagent E：第一部分（流程设计与执行）
- Subagent F：第二部分（协议适配）+ 第三部分（Lua API）
- Subagent G：第四部分（运行与配置）+ 第五部分（资源管理）
- Subagent H：第六部分（网络与状态）+ 第七部分（分布式）
- Subagent I：第八部分（监控与日志）+ 第九部分（前端技术栈）

**第四批：全局校验**（主 agent grep 验证）

### 4.2 Subagent 规范

每个 subagent 必须：
1. 接收具体源文件路径
2. 从代码读取实际内容，不使用旧 README 推断
3. 输出完整章节 markdown，主 agent 审核后写入
4. 发现 README 未覆盖的新功能时标注"新增功能"

---

## 五、全局校验清单

| # | 检查项 | 验证方法 |
|---|--------|---------|
| 1 | pattern 全覆盖 | flow.go const 数 == README 表行数 |
| 2 | binding type 全覆盖 | action.go resolveFieldValue case 数 == README |
| 3 | 节点类型全覆盖 | executor.go switch 分支数 == README |
| 4 | Lua 函数名准确 | script/api_*.go PreloadModule 注册名 == README |
| 5 | 配置字段完整 | Config struct json tag 数 == README |
| 6 | 适配器方法完整 | adapter.go interface 方法数 == README |
| 7 | 过滤器运算符完整 | CompareValues case 数 == README |
| 8 | 目录存在性 | README 每个目录 ls 确认 |
| 9 | TS-Go 类型一致 | types/*.ts interface 字段与 Go struct json tag 对应 |
| 10 | 章节交叉引用 | 锚点引用的节确实存在 |

---

## 六、常见错误

| 错误 | 后果 | 预防 |
|------|------|------|
| 凭上次记忆写 | 漏掉新增功能 | 每次都从代码审查 |
| 前后端拆开描述 | 同一功能信息分散 | 每个模块前后端一体 |
| pattern 用别名 | 配置无效 | 从 flow.go const 读取 |
| Lua 名用 Go 名 | 文档与脚本不一致 | 从 PreloadModule 注册读取 |
| 只写后端不写 UI | 用户不知道怎么操作 | 每个功能描述含 UI 操作 |
| 配置和流程分开太远 | 用户找不到 | 第四部分集中运行配置 |

---

## 七、执行清单

每次更新 README：

- [ ] **代码审查**：按第三章逐项 grep 读取源码当前状态
- [ ] **差异分析**：比对审查结果与现有 README，输出差异报告
- [ ] **用户确认**：展示报告，确认更新范围和结构调整
- [ ] **分批执行**：按第四章方案并行更新
- [ ] **结构调整**：按第二章 9 部层级化组织，每个模块前后端一体
- [ ] **全局校验**：按第五章清单逐项 grep 验证
- [ ] **用户终审**：展示完整变更后写入文件
