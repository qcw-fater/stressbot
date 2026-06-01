package robot

import (
	"net"
	"net/http"
	"time"
)

// newRobotHTTPClient 返回 Robot 独占连接池的 HTTP 客户端。
//
// 压测语义上每个 Robot 模拟一个独立客户端，Transport 也按 Robot 隔离，避免全进程
// 共享连接池把大量账号揉成同一个“超级 HTTP 客户端”，导致连接池状态互相影响。
func newRobotHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          4,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			DisableCompression:    true,
		},
	}
}
