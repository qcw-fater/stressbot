# 监控汇总单一数据源设计

## 目标

监控后端负责计算单动作和跨动作指标，实时页、节点卡片、历史采样与报告只消费后端结果，不再各自加权计算 Apdex 或百分位数。

## 后端契约

`monitor.CollectorSnapshot` 新增：

- `timingDetail`: 实际采集级别，取值 `rtt`、`codec`、`full`。
- `summary`: 跨动作汇总，包含结果计数、成功率、RTT Apdex 及其完整分母、RTT/监听等待/总耗时合并直方图、客户端阶段平均值和 QPS。

`ActionSnapshot` 新增 `rttApdexSampleCount`，显式表示 `rttSampleCount + rttFailedCount`。失败请求即使没有 RTT 也能向前端表达“有 Apdex 分数但没有 RTT 分位数”。

本机快照和 `MergeSnapshots` 都调用同一个汇总函数。跨动作 P95/P99 通过合并直方图桶后重算；不得平均各动作的 P95/P99。分布式 `timingDetail` 取所有有效快照的最低级别，避免展示部分节点未采集的字段。

## 前端行为

- 实时总览、节点卡片、历史详情和报告读取 `snapshot.summary`。
- 单动作 Apdex 是否可用由 `kind=networked && rttApdexSampleCount>0` 判断；RTT 延迟是否可用仍由 `rttSampleCount>0` 判断。
- “高级诊断”改为“耗时拆分”。取消数是结果字段，按调用方配置直接显示，不受拆分开关控制。
- `rtt` 展示非 RTT 与发送耗时；`codec` 追加编码/解码；`full` 再追加构建、解码排队、分发等待、解析/写状态。未采集字段不显示为 0。

## 历史与兼容

Admin 时序采样直接读取 `summary`。历史最终快照投影保留 `summary` 和 `timingDetail`。当前后端在收到旧快照时从已有 action 原始字段补建 summary；老归档缺少原始失败分母时只能保持其已有精度，不在前端恢复第二套算法。

## 冗余清理

删除没有路由或导入方的旧监控 tabs，以及只为前端聚合存在的 `computeWeightedMetrics`。保留 `timingDetail` 配置，因为它控制热路径额外计时开销，与 Apdex 阈值和算法无关。

## 验证

- Go 测试覆盖失败分母、跨动作直方图重算、最低采集级别、历史采样读取 summary。
- Vitest 覆盖实时模型纯映射、失败-only Apdex 与耗时拆分列。
- 执行 Go 全量构建、TypeScript 编译、前端全量测试；后端改动后按仓库约定运行 Agent 并审查日志。
