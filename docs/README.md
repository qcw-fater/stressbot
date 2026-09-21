# stressbot 技术知识库

本目录是 stressbot 的**技术设计文档库**：每份文档对照实际代码编写、随代码演进维护，反映**已实施状态**（不是愿望清单）。历史设计过程归档在 `docs/superpowers/` 与 `docs/modernization/`，不随代码更新，仅作考古参考。

用户侧使用手册见[根 README](../README.md)；前端专项见 [cmd/web/README.md](../cmd/web/README.md)；仓库级开发约定见 [AGENTS.md](../AGENTS.md)。

---

## 文档地图

| 领域 | 文档 | 内容 |
| --- | --- | --- |
| 总览 | [architecture.md](architecture.md) | 三进程模型、分层依赖、单次动作数据流、构建与发布 |
| 配置体系 | [configuration.md](configuration.md) | 三进程 TOML 全字段、加载管线与校验、时长契约、环境变量、单机生命周期与 runner 资源链 |
| 流程引擎 | [flow-node-system.md](flow-node-system.md) | 节点 / pattern / binding / 过滤器 / 条件表达式 / onError / 心跳 |
| 协议适配 | [adapter-layer.md](adapter-layer.md) | 声明式 codec schema 全字段、pipeline 与算法注册表、UDP 偏移加密 |
| 动态 proto | [proto-schema.md](proto-schema.md) | .proto 动态加载、wire 数据结构族、内容寻址去重、FrozenCache 回落、正确性防线 |
| Lua 运行时 | [lua-runtime.md](lua-runtime.md) | 运行时池与协程模型、87 函数 API、get/get_view 契约、wire-first 消费路径、内存优化结论 |
| 网络层 | [network-layer.md](network-layer.md) | gnet 集成、请求-响应与监听路由、连接级心跳、机器人管理与渐进加压 |
| 状态层 | [state-store.md](state-store.md) | 单机器人状态存储、路径与 Overlay、过滤器比较、Redis 共享态与租约锁 |
| 监控 | [monitoring-system.md](monitoring-system.md) | 指标采集、DDSketch 延迟分布、Apdex 口径、动作分类、分布式聚合、导出 |
| 错误码 | [error-code-system.md](error-code-system.md) | 29 个框架码全表、码段契约、errors.json 撞码防线 |
| 日志 | [logging.md](logging.md) | 结构化文件日志、字段约定、告警、外部采集栈 |
| 分布式总览 | [distributed-architecture.md](distributed-architecture.md) | Admin / Agent 职责划分、gRPC 控制面、任务生命周期、集群拓扑 |
| Admin 内幕 | [admin-implementation.md](admin-implementation.md) | 核心组件、HTTP 管理面、MySQL 归档、分配策略 |
| Agent 内幕 | [agent-implementation.md](agent-implementation.md) | 会话与租约、内容寻址资源包、任务执行、指标上报 |
| 分布式容错 | [distributed-error-handling.md](distributed-error-handling.md) | 断连 / 租约失效 / 重启恢复 / 命令可靠投递 |
| 前端编辑器 | [visual-flow-editor.md](visual-flow-editor.md) | FlowEditor 架构、画布交互、校验体系、持久化 |
| 前端 API 层 | [frontend-api.md](frontend-api.md) | OpenAPI 契约与生成客户端、服务层约定、轮询策略 |
| 资源同步 | [resource-sync.md](resource-sync.md) | IndexedDB 工作副本、基线同步、任务下发链路 |
| 渐进加压 | [ramp-up.md](ramp-up.md) | 阶段模型、reset 段落切分、历史归档口径 |
| 部署运维 | [deployment.md](deployment.md) | 部署拓扑、构建与发布、Docker 演示栈、conf/deploy/ 中间件配置导读、进程守护与升级注意 |

## 按角色阅读路径

- **新人上手**：architecture.md → flow-node-system.md → 根 README 快速开始跑一次单机压测
- **压测配置作者**：根 README（flow.json / Lua API 参考）→ flow-node-system.md → configuration.md（单机配置）→ ramp-up.md
- **协议适配开发者**：adapter-layer.md → proto-schema.md → network-layer.md → conf/adapter/ 现成样例
- **框架 / 引擎开发者**：flow-node-system.md → lua-runtime.md → network-layer.md → state-store.md → proto-schema.md → monitoring-system.md → error-code-system.md
- **分布式运维**：distributed-architecture.md → admin-implementation.md → agent-implementation.md → configuration.md → deployment.md → logging.md
- **前端开发者**：cmd/web/README.md → visual-flow-editor.md → frontend-api.md → resource-sync.md

## 维护约定

- 文档与代码不一致时**以代码为准**，并尽快修文档；改代码的 PR 应同步触碰相关文档。
- 每份文档首部标注「对照实际代码编写」及核对过的代码范围。
- 废弃能力从文档中物理删除（与代码约定一致），不留过时描述；仅在历史归档中可保留当时状态。
- `docs/superpowers/`、`docs/modernization/` 为历史归档：内容反映写作当时的状态，**不**随代码维护，检索时注意时效。
