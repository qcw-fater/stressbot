package admin

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"stressbot/utils"
	"time"

	"github.com/cenkalti/backoff/v4"
	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

func agentEndpoint(baseURL string, path ...string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("解析节点地址: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("节点地址 scheme 必须是 http 或 https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("节点地址缺少 host")
	}
	return u.JoinPath(path...).String(), nil
}

// drainAndClose 关闭响应体前先排空剩余字节，让 net/http 能把连接放回 keep-alive 空闲池复用。
//
// net/http 仅在响应体读到 EOF 后再 Close 才会复用底层 TCP；未读完就 Close 会直接丢弃连接。
// Admin→Agent 是「任务下发/停止 + 版本查询」的高频同 host RPC，不复用会持续新建/关闭 TCP，
// 浪费 FD 与 ephemeral port。用 LimitReader 兜底：控制面响应都很小，异常超大 body 放弃排空
// （退化为不复用），不拖住调用方。
func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// AgentDispatcher Admin → Agent HTTP 通信。
type AgentDispatcher struct {
	httpClient *http.Client
}

func NewAgentDispatcher() *AgentDispatcher {
	return NewAgentDispatcherWithTLS(nil)
}

func NewAgentDispatcherWithTLS(tlsConfig *tls.Config) *AgentDispatcher {
	transport := newAgentHTTPTransport(tlsConfig)
	return &AgentDispatcher{
		httpClient: &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

func newAgentHTTPTransport(tlsConfig *tls.Config) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = 15 * time.Second
	transport.MaxIdleConns = 64
	transport.MaxIdleConnsPerHost = 32
	transport.TLSClientConfig = tlsConfig
	return transport
}

// AssignTask 向 Agent 推送任务分配。
func (d *AgentDispatcher) AssignTask(addr string, assignment TaskAssignment) error {
	return d.post(addr, "/agent/v1/task", assignment, 2)
}

// Stop 向 Agent 发送停止命令。
func (d *AgentDispatcher) Stop(addr, taskID string) error {
	return d.post(addr, "/agent/v1/stop", map[string]string{"taskId": taskID}, 2)
}

// Shutdown 向 Agent 发送关闭命令（终止进程）。
func (d *AgentDispatcher) Shutdown(addr string) error {
	return d.post(addr, "/agent/v1/shutdown", nil, 1)
}

// Version 查询 Agent 版本。
func (d *AgentDispatcher) Version(addr string) (string, error) {
	resp, err := d.get(addr, "/agent/v1/version")
	if err != nil {
		return "", err
	}
	defer drainAndClose(resp)
	var result struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Version, nil
}

func (d *AgentDispatcher) post(addr, path string, body any, retries int) error {
	endpoint, err := agentEndpoint(addr, path)
	if err != nil {
		return err
	}

	// 序列化 body 一次（每次重试复用同一份字节，避免重复 marshal）
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
	}

	// 退避策略：1s→10s 带 jitter，最多重试 retries 次。
	// 原手写实现对所有非 2xx（含 4xx）都重试——4xx 是永久性错误（请求格式错/路径错/未授权），
	// 重试只是浪费时间并掩盖配置 bug，故用 backoff.Permanent 标记后立即停止。
	b := backoff.WithMaxRetries(utils.NewExponentialBackOff(utils.RetryPolicy{
		Initial: time.Second,
		Max:     10 * time.Second,
		Factor:  2,
		Jitter:  0.5,
	}), uint64(retries))
	var attempt int

	notify := func(err error, wait time.Duration) {
		stresslog.Warn("[DISPATCHER] POST 失败，将重试",
			zap.String("addr", addr),
			zap.String("path", path),
			zap.String("url", endpoint),
			zap.Int("attempt", attempt),
			zap.Int("maxRetries", retries),
			zap.Duration("backoff", wait),
			zap.Error(err))
	}

	err = utils.RetryWithStop(nil, func() error {
		attempt++
		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequest("POST", endpoint, bodyReader)
		if err != nil {
			// 请求构造失败是永久性错误（URL 格式错等），不重试
			return backoff.Permanent(fmt.Errorf("create request: %w", err))
		}
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := d.httpClient.Do(req)
		if err != nil {
			// 网络错误（连接拒绝/超时）：Agent 可能正在重启，值得重试
			return fmt.Errorf("post %s: %w", endpoint, err)
		}
		drainAndClose(resp)

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		// 4xx 是永久性错误（请求格式错/路径不存在/未授权），重试无意义，立即停止。
		// 5xx 是服务端临时故障，正常返回（可重试）。
		httpErr := fmt.Errorf("agent returned status %d", resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return backoff.Permanent(httpErr)
		}
		return httpErr
	}, notify, b)

	return err
}

func (d *AgentDispatcher) get(addr, path string) (*http.Response, error) {
	endpoint, err := agentEndpoint(addr, path)
	if err != nil {
		return nil, err
	}
	return d.httpClient.Get(endpoint)
}
