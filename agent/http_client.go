package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// errNotRegistered Admin 返回 404 时表示 Agent 未注册（可能 Admin 重启了）。
var errNotRegistered = errors.New("agent not registered on admin")

// AdminClient 与 Admin 服务器通信的 HTTP 客户端。
//
// 所有请求（注册/心跳/上报/任务完成/拉取任务）共用单一 http.Client，
// timeout 由调用方通过 ResolvedConfig.RequestTimeout 注入；
// 调用方 ctx 取消（如 Agent shutdown）能立刻打断阻塞中的请求。
type AdminClient struct {
	base    string // "http://admin:8080"
	agentID string
	client  *http.Client
}

// NewAdminClient 创建 Admin 客户端。
// timeout 用于单次 HTTP 请求；ctx 取消优先于 timeout。
func NewAdminClient(baseURL, agentID string, timeout time.Duration) *AdminClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &AdminClient{
		base:    baseURL,
		agentID: agentID,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// SetAgentID 更新 agentID（注册成功后设置）。
func (c *AdminClient) SetAgentID(id string) {
	c.agentID = id
}

// AgentID 返回当前 agentID。
func (c *AdminClient) AgentID() string {
	return c.agentID
}

// Register 向 Admin 注册。
func (c *AdminClient) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal register request: %w", err)
	}

	resp, err := c.doPost(ctx, "/sbot/agent/register", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("register failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var result RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode register response: %w", err)
	}
	return &result, nil
}

// Heartbeat 发送心跳。
func (c *AdminClient) Heartbeat(ctx context.Context, req HeartbeatRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal heartbeat: %w", err)
	}

	resp, err := c.doPost(ctx, "/sbot/agent/"+c.agentID+"/heartbeat", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusNotFound {
			return errNotRegistered
		}
		return fmt.Errorf("heartbeat failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return nil
}

// PostStress 上报压测指标。
func (c *AdminClient) PostStress(ctx context.Context, report StressReport) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal stress report: %w", err)
	}

	resp, err := c.doPost(ctx, "/sbot/agent/stress", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post stress failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return nil
}

// PostSystem 上报系统指标。
func (c *AdminClient) PostSystem(ctx context.Context, report SystemReport) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal system report: %w", err)
	}

	resp, err := c.doPost(ctx, "/sbot/agent/system", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post system failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return nil
}

// FetchPendingTask 拉取待执行任务（回退通道）。
func (c *AdminClient) FetchPendingTask(ctx context.Context) (*TaskAssignment, error) {
	url := c.base + "/sbot/agent/" + c.agentID + "/pending-task"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 204 {
		return nil, nil // 无任务
	}
	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetch pending task failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var task TaskAssignment
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return nil, fmt.Errorf("decode pending task: %w", err)
	}
	return &task, nil
}

// ReportTaskDone 上报任务完成。
func (c *AdminClient) ReportTaskDone(ctx context.Context, report TaskCompletionReport) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal task done: %w", err)
	}

	url := fmt.Sprintf("/sbot/agent/%s/task/%s/done", c.agentID, report.TaskID)
	resp, err := c.doPost(ctx, url, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("report task done failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return nil
}

// Deregister 注销（best-effort，不重试）。
func (c *AdminClient) Deregister(ctx context.Context) error {
	body, _ := json.Marshal(DeregisterRequest{AgentID: c.agentID})
	url := "/sbot/agent/" + c.agentID + "/deregister"

	resp, err := c.doPost(ctx, url, body)
	if err != nil {
		return err // 不重试
	}
	resp.Body.Close()
	return nil
}

// DownloadFile 下载文件到 writer（升级用）。
func (c *AdminClient) DownloadFile(ctx context.Context, url string, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: status=%d", resp.StatusCode)
	}
	_, err = io.Copy(dst, resp.Body)
	return err
}

func (c *AdminClient) doPost(ctx context.Context, path string, body []byte) (*http.Response, error) {
	url := c.base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.client.Do(req)
}

// RetryWithRetriesAndBackoff 在 RetryWithBackoff 之上额外支持"最大重试次数"。
//   - maxRetries < 0  ：无限重试（直到 ctx 取消）
//   - maxRetries == 0 ：当成 1 次（仅尝试一次，不重试）
//   - maxRetries > 0  ：最多重试 maxRetries 次（即总共最多 maxRetries+1 次尝试）
//
// initial / max 控制指数退避区间。返回最后一次失败的错误。
func RetryWithRetriesAndBackoff(ctx context.Context, op func() error, initial, max time.Duration, maxRetries int, desc string) error {
	if initial <= 0 {
		initial = time.Second
	}
	if max <= 0 {
		max = 60 * time.Second
	}
	backoff := time.Duration(0)
	attempt := 0
	for {
		err := op()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// maxRetries: <0 无限；其他比较"已重试次数"
		if maxRetries >= 0 && attempt >= maxRetries {
			return fmt.Errorf("%s: 已达最大重试次数 %d: %w", desc, maxRetries, err)
		}
		attempt++
		if backoff == 0 {
			backoff = initial
		} else {
			backoff *= 2
			if backoff > max {
				backoff = max
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
}

func nextBackoff(current, max time.Duration) time.Duration {
	if current == 0 {
		return time.Second
	}
	next := current * 2
	if next > max {
		return max
	}
	return next
}
