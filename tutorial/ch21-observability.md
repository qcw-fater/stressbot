# 第 21 章 观测与排障

> **目标**：读得懂指标背后的含义、查得出问题在哪——全书毕业考。面板操作见第 19 章，本章聚焦**解读与诊断**。
>
> **前置**：全书。素材均来自源码核实与前章实测。

## 21.1 指标精读

**动作 kind 四分类**（按运行时实际网络行为定型，前端按它选主指标列）：

| kind | 判定 | 主指标 |
|---|---|---|
| `networked` | 有网络往返（RTT 样本） | RTT |
| `listen` | 无 RTT 但监听命中 | 监听等待 |
| `send` | 无 RTT 有发送字节 | 发送耗时 |
| `local` | 都没有（setState/Lua 本地逻辑） | 无（显示 `--ms`） |

**Apdex 只对 networked 打分**：监听/发送/本地类显示"不适用"——这三类的耗时主体是服务端业务时长或客户端执行时长，掺进总分会让分数随动作构成漂移。分母是"发起过请求的样本"：超时与连接中断记 frustrated（拿不到 RTT 但确实是坏体验），业务错误按真实 RTT 正常打分。**总耗时（wallClock）只作诊断**（含 Lua sleep 与客户端调度延迟），不打分。

**监听三指标**：`ListenWait` 直方图（从帧到达内核算起）、`ListenReadyCount`（等待前已在队列）、`ListenTimeoutRate`（超时/（等待+ready+超时））——推送链路健康度看这一个率就够。

**错误单维聚合**：按 code 聚合，`code < 100` 标 `框架`、≥100 标 `业务`（第 9 章的码表 + errors.json 描述）。

## 21.2 日志与施压机自身健康

- 日志是结构化 JSON（`log.path` 文件）或彩色控制台；机器人标识字段是 `robot`；关注 `[ENGINE] 动作执行失败`（含 errorCode/errorDetail/strategy 全链）与 `[ROBOT]`/`[MANAGER]`/`[AGENT]` 系列；
- **P99 突刺但服务端无感，先查施压机**：`/debug/sched`（pprof 端口上，Go 调度延迟分位数——施压机自身过载会污染客户端测得的 P99/Apdex，压测中看 `sinceLast` 组）；CPU 打满时 P99 失真不是服务端劣化；
- `/debug/statekeys`（robot.get/get_path 按 key 计数，定位脚本整读热点）、`/debug/pprof`（常规画像）；`/metrics` 是 JSON（接 Prometheus 需中转）。

## 21.3 FAQ：常见问题对照

| 症状 | 诊断与解法 |
|---|---|
| 启动报 `缺少前缀` / `listenRefs 引用的 listen 无定义` / `撞框架保留段` | 加载期契约错误，第 5 章 5.8 对照表；界面上先看校验报告（第 17 章） |
| 大量 4（等响应超时）连锁出现 | **超时风暴**：上游失败后继续执行依赖请求制造级联超时。解法=上游动作配 `strategy: abort/skip` 或 handler 恢复分支（第 9 章），不要让失败链跑完 |
| 停止一批机器人，错误数暴增 | 取消码 6 归类 Canceled **不是故障**（第 9 章 9.7）；看错误面板时把取消列分开读 |
| tcpListen 永远超时（31） | 三选一：没在推送前 listenRefs 注册（声明式）/ 没 `ensure_tcp_listener`（Lua）/ 路由键与请求回执撞车（第 8 章 8.5） |
| 加密协议对端解不开、流量像明文 | 密钥长度不符时部分算法**静默不加密**（第 4 章 4.4 的 keyLen ≥ 陷阱）；核对密钥实际字节数 |
| 监控里 robots 运行数"阶梯"看不到（单机） | 单机监控在加压结束后才启动（第 12 章实测），阶梯看 `[MANAGER]` 日志或用分布式形态 |
| P99 高但 Apdex 尚可、服务端无感 | 先 `/debug/sched` 查施压机调度延迟；再查动作表 `非RTT`/`client` 列是否异常（客户端 CPU 开销） |
| Admin 重启后任务变 failed | 预期语义：`admin restart, task lost`（第 14 章）；Agent 若在跑会被孤儿停机叫停 |

## 21.4 导出

Console（周期表）/ HTTP JSON（`/metrics`）/ CSV（单机退出自动导出 `metrics/metrics_<时间戳>.csv`；监控面板也有导出按钮）/ pprof。历史报告导出见第 20 章。

## 21.5 毕业验收

- [ ] 能回答："P99 突刺但服务端无感，先查什么？"（施压机 `/debug/sched` → 动作表 kind 与非RTT 列 → 再怀疑服务端）
- [ ] 能对一个失败任务在 5 分钟内定位到：错误码分布 → 日志 errorCode 链 → 对应章节的解法
- [ ] 能设计一个带 onError 恢复分支的流程并解释每个样本在监控里的归类

## 延伸阅读

[docs/monitoring-system.md](../docs/monitoring-system.md)、[docs/logging.md](../docs/logging.md)、AGENTS.md「内存优化约束」的观测端点节。教程至此完结——回到 [目录](README.md)。
