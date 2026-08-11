# 控制面安全部署

Admin 管理面只监听 `127.0.0.1:7718`，由反向代理完成公网 HTTPS 与 OIDC/IAP 认证。Admin-Agent 内部控制面使用独立的 `7720` 端口；Agent 本地控制端口默认是 `7719`。

## 证书角色

- Admin 证书由 Admin CA 签发，证书必须包含实际 DNS/IP SAN。
- Agent 证书由 Agent CA 签发，证书必须包含实际 DNS/IP SAN。
- Admin 的 `peerCaFile` 指向 Agent CA；Agent 的 `peerCaFile` 指向 Admin CA。
- 不把 CA 私钥、叶子私钥或令牌提交到仓库。

启用 mTLS 时，Admin 配置示例为：

```json
{
  "controlPlane": {
    "listenHost": "0.0.0.0",
    "port": 7720,
    "publicUrl": "https://admin.internal.example:7720",
    "tls": {
      "certFile": "/etc/stressbot/tls/admin.crt",
      "keyFile": "/etc/stressbot/tls/admin.key",
      "peerCaFile": "/etc/stressbot/tls/agent-ca.crt"
    }
  }
}
```

Agent 的 `adminUrl` 指向上述 HTTPS 地址，`publicUrl` 也必须使用 HTTPS；TLS 配置使用 Agent 证书并信任 Admin CA。

## 两阶段切换

1. 先发布支持双监听和 TLS 配置的新二进制，保留 HTTP 兼容配置；进程每次启动会输出一次兼容模式警告。
2. 下发证书并确认 SAN、权限和时钟；把全部 Agent 与 Admin 切到 HTTPS/mTLS。错误 CA、无证书和过期证书测试通过后，再在网络层关闭旧 HTTP 控制面。

代码不会在 HTTPS 失败后自动降级到 HTTP。Supervisor、systemd、容器编排或 Windows 服务都可以托管前台进程；关键要求是有限重试、SIGTERM、进程组停止和可观察日志。
