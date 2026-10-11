# 端口与探活速查表（默认配置）

| 进程 | 端口 | 用途 | 探活方式 |
|---|---|---|---|
| admin | 7718 (HTTP) | 管理面 API（`/sbot/`）+ 静态页面 | `GET /sbot/system`（无专用 /healthz） |
| admin | 7720 (gRPC) | 控制面：Agent 长连接 | Agent 侧连通性由日志体现 |
| admin | — | pprof 默认不启用（admin.toml 里被注释） | — |
| agent | 6060 (HTTP) | pprof + `/metrics`（JSON）+ `/metrics/summary` | `GET <agent>:6060/metrics` |
| stressbot | 6060 (HTTP) | pprof | `GET localhost:6060/debug/pprof/` |
| stressbot | 6061 (HTTP) | 监控 JSON 端点（`[monitor.http]` 可选） | `GET localhost:6061/metrics` |

注意：

- 所有管理/调试端口**只绑内网或本机**，不要暴露公网；
- `/metrics` 返回的是 JSON 而非 Prometheus 文本格式，接 Prometheus 需 json_exporter 中转；
- 端口冲突时改各 toml 对应段（agent 与 stressbot 同机部署时注意 6060 冲突）。
