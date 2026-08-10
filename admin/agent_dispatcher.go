package admin

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

func normalizeAddr(addr string) string {
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	return addr
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
	return &AgentDispatcher{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
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
	url := fmt.Sprintf("http://%s%s", normalizeAddr(addr), path)

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
	b := backoff.WithMaxRetries(newDispatcherBackoff(1*time.Second, 10*time.Second), uint64(retries))
	var attempt int

	notify := func(err error, wait time.Duration) {
		stresslog.Warn("[DISPATCHER] POST 失败，将重试",
			zap.String("addr", addr),
			zap.String("path", path),
			zap.String("url", url),
			zap.Int("attempt", attempt),
			zap.Int("maxRetries", retries),
			zap.Duration("backoff", wait),
			zap.Error(err))
	}

	err := backoff.RetryNotify(func() error {
		attempt++
		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequest("POST", url, bodyReader)
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
			return fmt.Errorf("post %s: %w", url, err)
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
	}, b, notify)

	return err
}

func (d *AgentDispatcher) get(addr, path string) (*http.Response, error) {
	url := fmt.Sprintf("http://%s%s", normalizeAddr(addr), path)
	return d.httpClient.Get(url)
}

// newDispatcherBackoff 构造 Admin→Agent RPC 的指数退避（带 jitter）。
// 与 agent.newExponentialBackoff 同构，但分属两个包各自维护（admin 不依赖 agent）。
func newDispatcherBackoff(initial, max time.Duration) *backoff.ExponentialBackOff {
	b := backoff.NewExponentialBackOff(
		backoff.WithInitialInterval(initial),
		backoff.WithMaxInterval(max),
	)
	b.MaxElapsedTime = 0
	b.RandomizationFactor = 0.5
	b.Multiplier = 2.0
	return b
}
