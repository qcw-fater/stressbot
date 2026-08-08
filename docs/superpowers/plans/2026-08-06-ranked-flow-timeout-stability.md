# 排位流程超时稳定性实施计划

## 目标

消除旧匹配推送引发的 288 和 `ListenStartLoading` 级联超时，使排位流程在 200 人并发下连续运行至少 30 分钟且业务链路持续周转。

## 任务

1. 在 FlowEditor validation 目录新增排位流程集成测试：读取 `rank.json` 与相关 Lua，先证明当前脚本缺少新鲜度校验、超时止损和 288 恢复。
2. 修改 `match_succeed.lua`：真实总截止时间、精确 teamId 硬校验、playerStatus 仅诊断、旧 `3:1` 丢弃。
3. 修改 `wait_match_enter_room.lua`：超时返回原始 err。
4. 修改 `listen_start_loading.lua`：真实总截止时间、当前 battleSession 校验、通过后原子式写入状态。
5. 修改 `ranked_leave_team.lua`：288/311 统一取消匹配恢复。
6. 精确修改 `rank.json` 的 `BattleLoadOK` 节点，增加有限重试和最终 skip，不格式化或覆盖其他现有配置改动。
7. 运行聚焦 Vitest、流程配置校验、Go 编译、TypeScript 编译、全量 Vitest。
8. 检查当前进程/端口和账号范围；用未使用的新账号启动 200 人排位任务，运行至少 30 分钟。
9. 采集动作指标和客户端/逻辑服/房间服/战斗服日志；如仍有流程性超时，回到失败用例并迭代，全部达标后交付。
