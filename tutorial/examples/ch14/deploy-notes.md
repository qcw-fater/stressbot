# 第 14 章组网检查单

## 一台 Admin + N 台 Agent 的最小组网

```
机器 A（服务器）  admin + MySQL(+Redis 可选)   ← admin 与 MySQL 可不同机，改 admin.toml 即可
机器 B（节点）    agent + 可达目标游戏服务器
机器 C（节点）    agent + 可达目标游戏服务器
浏览器            访问 admin 的 7718（经 nginx /stressbot/ 子路径或内网直连）
```

## 逐步检查单

1. **MySQL**：`[mysql] host` 非空时 admin 启动第一步连库并自动建 11 张表，连不上直接退出（实测文案：
   `连接 MySQL 失败: dial tcp 127.0.0.1:3306: ...`）。库可以是空库，不要手工建表。`host` 留空则历史与模板库不可用，admin 仍能启动。
2. **Redis 按需**：只有任务脚本用 `share` 时需要；未配置时启动带 share 脚本的任务会被拒
   （`任务脚本使用了共享状态(share)，但服务器未配置 Redis，无法启动`）。
3. **admin.toml 关键项**：`server.port=7718`、`controlPlane.port=7720`、`staticDir` 指向
   实际前端产物目录（发布包是包根 `dist/`，源码运行是 `cmd/web/dist`）。
4. **agent.toml 关键项**：`id` 全集群唯一、`adminAddress` 指向 admin 的 **7720**（gRPC，
   不是 7718）、`maxBots` 按机器容量填（任务分配按它算）。
5. **Agent 启动验证**（admin 未起时的实测日志）：
   `[AGENT] 启动中 agentID=... adminAddress=... reconnectMaxRetries=-1`
   → `[AGENT] gRPC 控制面不可用，准备重连 retryAttempt=1 backoff=2.4s` → 指数退避自动重试。
   只要这条链路在跳，说明 agent 本身健康。
6. **节点上线确认**：admin 起来后 agent 自动注册（Hello/Welcome 握手）；前端"节点管理"
   看到节点 idle、容量正确。
7. **失联演练**：kill 一台 agent → 60 秒后 Admin 判 offline 自动清理注册表；
   任务运行中单节点丢失**不会**停任务，全部节点失效才自动停止。
8. **时长参数关系**（默认）：心跳 10s < 租约 45s < 离线判定 60s——Agent 失联 45 秒后
   自行取消当前任务（比 Admin 判离线更早，避免压测孤儿流量）。
