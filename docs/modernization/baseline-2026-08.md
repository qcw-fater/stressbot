# 成熟化改造基线（2026-08）

本文记录 M0 的可复现基线和停止条件。所有结果来自 2026-08-11 的本地 `master` 工作区；不记录数据库口令、证书私钥、访问令牌或完整生产配置。

## 1. 代码与工具基线

- Git HEAD：`3c44c4441bebf6f0c9cc5e705b15a5e1ec26e6c9`
- Go：`go1.26.0 windows/amd64`
- Node.js：`v24.15.0`
- npm：`11.12.1`
- 操作系统：`Microsoft Windows NT 10.0.19045.0`
- 可见逻辑处理器：6

配置快照只保存 SHA-256，用于确认复测输入是否一致：

| 文件 | SHA-256 |
|---|---|
| `conf/admin-config.json` | `9D2D3F1CD293B3ABC5395BC0B25A1F33D8EE557935B52E3609D7550A606AE89A` |
| `conf/agent-config.json` | `00CEC8AEC1BDB4CDB1DED7061D56E793B86119E440680B4B8584FA16894BFD29` |
| `conf/config.json` | `3318F1CB5FB50C57040E916B05D6D30E6E618FEB49EC00696946EA24FB055F0E` |
| `conf/flow/flow.json` | `F1FD5850773FCBB1FB29B5A377741B5F596FCAEA14E05EB9E0A0EEDBF09B3395` |

## 2. 自动化验证基线

所有 Go 命令使用仓库内可写缓存 `D:\Gitee\stressbot\.tmp\gocache`。

| 验证 | 结果 | 墙钟时间 |
|---|---|---:|
| `go build ./...` | 通过 | 34.535s |
| `go test ./...` | 全部通过 | 35.881s |
| `npx.cmd tsc -b` | 通过 | 45.906s |
| `npm.cmd run test` | 78 个测试文件、607 个测试全部通过 | 145.573s |

Vitest 自身报告的执行时间为 143.36s；上表墙钟时间还包含 npm 启动和退出开销。

## 3. 运行与性能基线

本机检查时 `127.0.0.1:3306`、`127.0.0.1:6379`、`127.0.0.1:7718` 均未监听。因此以下数据不能在 M0 本地会话中可靠取得：

| 指标 | M0 状态 | 后续取得方式 |
|---|---|---|
| Admin 空闲 CPU/RSS | 未测；MySQL、Redis 未就绪，Admin 无法进入稳定空闲态 | 在预发布环境启动完整依赖，预热 5 分钟后连续采样 10 分钟 |
| 10k Robot 条件表达式 CPU/alloc | 未测；仓库当前没有 condition benchmark | 保留到 M6 决策项目，不在 M0 提前实现 CEL/AST benchmark |
| 10k 空闲 listen wakeup | 未测；仓库当前没有 listen wakeup benchmark | 保留到 M6 事件化 listen 决策项目 |
| 前端 60 秒请求次数 | 未测；Admin 未运行，不能得到真实 observer/轮询行为 | M5 前在预发布环境分别记录 idle、running、finalReport 三种模式 |
| MySQL 表、列、索引快照 | 未测；3306 未监听 | 首次 Goose 预发布演练前从 `information_schema` 导出不含数据和凭据的结构快照 |

M5 本地补测已用浏览器 fake timer 运行完整 60 秒，并由真实浏览器打开无数据库/Redis 的本地 Admin 页面做冒烟检查：

| 模式 | 60 秒请求调度结果 |
|---|---|
| `edit` | `/agents` 7 次、`/system` 7 次；task/stress 0 次 |
| `running` | task/stress/system/agents 各 13 次（首次立即请求 + 每 5 秒一次） |
| `finalReport` | 四组请求均为 0 次 |

同一 query 的并发读取合并测试、切换 taskId/disabled 的 AbortSignal 测试、节点面板无第二个 interval 的代码检查均通过。本地真实浏览器页面无 console warning/error。以上结果足以验证前端调度实现，但不冒充预发布流量基线；旧 `usePolling.ts` 因此保留为未导出、零引用的回滚参考。

这些缺口不阻止 M1–M5 的测试驱动开发，但会关闭相应的发布门禁：没有补齐真实数据库快照，不允许在生产启用 Goose `auto`；没有预发布真实浏览器请求计数和完整观察窗口，不物理删除旧 `usePolling.ts`；M6 的三个项目仍需单独讨论和量化签字。

## 4. 统一回滚原则

1. 应用改造使用“上一版本二进制 + 上一版本配置”回滚。每个里程碑独立提交，不把数据库、安全、协议和前端状态迁移混在一个提交中。
2. 数据库 migration 失败即停止 Admin 启动；生产环境不自动执行 `down`。回退依赖迁移前备份/快照、旧应用二进制和新的前向修复 migration。
3. 证书切换至少保留上一组证书一个发布窗口。新旧控制面兼容发布完成、错误证书和无证书测试通过后，才能关闭旧入口。
4. OpenAPI、JSON Schema 和 TanStack Query 在删除旧 DTO、手写校验或 `usePolling` 前，至少经过一个预发布窗口；生成物必须可重复生成且无 diff。
5. Lua Worker、RC4、backoff 和 Zundo 均以兼容测试为回滚依据；行为不一致时回滚本里程碑提交，不通过兼容开关长期维护双实现。

## 5. 停止条件

- 基线编译或测试出现新增失败：停止进入下一里程碑。
- 发现改动需要覆盖用户现有工作区文件但无法确认归属：停止并请求确认。
- mTLS 尚未完成证书签发、轮换和负例演练：不得关闭兼容入口。
- MySQL 没有结构快照和可恢复备份：不得执行生产 migration。
- OpenAPI/Schema 不能完整描述错误响应或领域语义：不得删除现有校验。
- Query 产生重复轮询、取消被计为断线或终态重复通知：不得删除旧轮询路径。

## 6. M0 结论

- 代码开发基线：通过。
- 本地自动化基线：通过。
- 预发布/生产运行基线：因外部服务未就绪而待补，相关发布门禁保持关闭。
- M1–M5 可以在测试和构建约束下继续实施；M6 未授权，不开展实现或 benchmark 文件建设。
