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
type AdminClient struct {
	base    string // "http://admin:8080"
	agentID string
	client  *http.Client
}

// NewAdminClient 创建 Admin 客户端。
func NewAdminClient(baseURL, agentID string) *AdminClient {
	return &AdminClient{
		base:    baseURL,
		agentID: agentID,
		client: &http.Client{
			Timeout: 10 * time.Second,
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

	resp, err := c.doPost(ctx, "/api/agent/register", body)
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

	resp, err := c.doPost(ctx, "/api/agent/"+c.agentID+"/heartbeat", body)
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

	resp, err := c.doPost(ctx, "/api/agent/stress", body)
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

	resp, err := c.doPost(ctx, "/api/agent/system", body)
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
	url := c.base + "/api/agent/" + c.agentID + "/pending-task"

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

	url := fmt.Sprintf("/api/agent/%s/task/%s/done", c.agentID, report.TaskID)
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
	url := "/api/agent/" + c.agentID + "/deregister"

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

// RetryWithBackoff 对给定操作执行指数退避重试。
// maxInterval 为退避上限，maxTotal 为总超时（0 = 无限重试）。
// 返回 nil 表示成功，非 nil 表示最终失败。
func RetryWithBackoff(ctx context.Context, op func() error, maxInterval, maxTotal time.Duration, desc string) error {
	var backoff time.Duration
	deadline := time.Time{}
	if maxTotal > 0 {
		deadline = time.Now().Add(maxTotal)
	}

	for {
		err := op()
		if err == nil {
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("%s: 重试超时 (%s): %w", desc, maxTotal, err)
		}

		backoff = nextBackoff(backoff, maxInterval)
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
