package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	json "stressbot/utils/jsonx"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// errNotRegistered Admin 返回 404 时表示 Agent 未注册（可能 Admin 重启了）。
var errNotRegistered = errors.New("agent not registered on admin")

// AdminClient 与 Admin 服务器通信的 HTTP 客户端。
//
// 所有请求（注册/心跳/上报/任务完成/拉取任务）共用单一 http.Client：
//   - 通用 timeout 由调用方通过 ResolvedConfig.RequestTimeout 注入；
//   - 心跳通过 ctx.WithTimeout(HeartbeatTimeout) 单独缩短到秒级，
//     避免一次心跳卡到 30s 才返回 → 长时间无感知 Admin 状态；
//   - 调用方 ctx 取消（如 Agent shutdown）能立刻打断阻塞中的请求。
type AdminClient struct {
	base       string // "http://admin:7718"
	agentID    string
	client     *http.Client
	hbReqLimit time.Duration // 心跳单次请求超时上限（取 min(HeartbeatTimeout, Timeout)）
}

// NewAdminClient 创建 Admin 客户端。
//
// 关键：自定义 Transport 而不用 DefaultTransport——
//   - DefaultTransport 走 ProxyFromEnvironment，Windows 会查 IE 注册表，
//     每次请求都有 syscall 开销；本机通信不需要走代理。
//   - DefaultTransport 没设 DialContext.Timeout，握手依赖 OS 内核
//     （Windows ~21s 才返回 timeout），看不到快速失败。
//   - 默认 MaxIdleConnsPerHost=2 对 admin 这种"高频心跳+上报+下载"的同一 host
//     场景偏紧；调大池子让 keep-alive 真正生效，减少不必要的 connect/close 抖动。
//     （本地多开 agent 互相争抢 ephemeral port 是测试环境问题，工具本身做好该做的。）
func NewAdminClient(baseURL, agentID string, timeout, hbReqTimeout time.Duration) *AdminClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if hbReqTimeout <= 0 || hbReqTimeout > timeout {
		hbReqTimeout = timeout
	}
	return &AdminClient{
		base:       baseURL,
		agentID:    agentID,
		client:     &http.Client{Timeout: timeout, Transport: newAdminTransport()},
		hbReqLimit: hbReqTimeout,
	}
}

// newAdminTransport 构造 agent → admin 专用 HTTP transport。
func newAdminTransport() *http.Transport {
	return &http.Transport{
		Proxy: nil, // 跳过 proxy lookup（本机通信无需走代理）
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,  // 显式 dial 超时，避免 Windows 内核 ~21s 慢失败
			KeepAlive: 30 * time.Second, // TCP keep-alive 探测，及时发现死连接
		}).DialContext,
		ForceAttemptHTTP2:     false, // 本机 HTTP/1.1 keep-alive 足够，避免 HTTP/2 协商开销
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32, // 默认 2 偏紧，提到 32 让心跳/上报/下载真正复用 keep-alive
		MaxConnsPerHost:       0,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
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
	defer drainAndClose(resp)

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body) // HTTP 错误响应体，ReadAll 失败不影响错误返回
		return nil, fmt.Errorf("register failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var result RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode register response: %w", err)
	}
	return &result, nil
}

// Heartbeat 发送心跳。
//
// 单次请求超时被压到 hbReqLimit（默认 5s）：
//   - 心跳是状态探测，应该快失败快重试；
//   - 用通用 30s timeout 会让一次失败卡 30s 才返回 → agent 长时间无感知 Admin 状态。
//
// 仍共用同一个 http.Client / Transport，避免多一个 keep-alive 池占额外 FD。
func (c *AdminClient) Heartbeat(ctx context.Context, req HeartbeatRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal heartbeat: %w", err)
	}

	hbCtx, cancel := context.WithTimeout(ctx, c.hbReqLimit)
	defer cancel()

	resp, err := c.doPost(hbCtx, "/sbot/agent/"+c.agentID+"/heartbeat", body)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body) // HTTP 错误响应体，ReadAll 失败不影响错误返回
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
	defer drainAndClose(resp)

	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		respBody, _ := io.ReadAll(resp.Body) // HTTP 错误响应体，ReadAll 失败不影响错误返回
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
	defer drainAndClose(resp)

	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		respBody, _ := io.ReadAll(resp.Body) // HTTP 错误响应体，ReadAll 失败不影响错误返回
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
	defer drainAndClose(resp)

	if resp.StatusCode == 204 {
		return nil, nil // 无任务
	}
	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body) // HTTP 错误响应体，ReadAll 失败不影响错误返回
		return nil, fmt.Errorf("fetch pending task failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var task TaskAssignment
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return nil, fmt.Errorf("decode pending task: %w", err)
	}
	if err := task.Validate(); err != nil {
		return nil, fmt.Errorf("invalid pending task: %w", err)
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
	defer drainAndClose(resp)

	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		respBody, _ := io.ReadAll(resp.Body) // HTTP 错误响应体，ReadAll 失败不影响错误返回
		return fmt.Errorf("report task done failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return nil
}

// Deregister 注销（best-effort，不重试）。
func (c *AdminClient) Deregister(ctx context.Context) error {
	body, _ := json.Marshal(DeregisterRequest{AgentID: c.agentID}) // 简单 struct 序列化不会失败
	url := "/sbot/agent/" + c.agentID + "/deregister"

	resp, err := c.doPost(ctx, url, body)
	if err != nil {
		return err // 不重试
	}
	drainAndClose(resp)
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
	defer drainAndClose(resp)

	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: status=%d", resp.StatusCode)
	}
	_, err = io.Copy(dst, resp.Body)
	return err
}

// drainAndClose 在关闭响应体前排空剩余字节，让 net/http 能把该连接放回 keep-alive 空闲池复用。
//
// net/http 仅在响应体读到 EOF 后再 Close 才会复用底层 TCP 连接；若未读完就 Close，
// 连接会被直接丢弃。Agent→Admin 是「秒级心跳 + 高频上报」的同 host 场景，不复用会导致
// 持续新建/关闭 TCP，浪费 FD 与 ephemeral port（正是自定义 Transport 调大空闲池想避免的）。
// 用 LimitReader 兜底：正常控制面响应都很小，异常超大 body 则放弃排空（退化为不复用），不拖住调用方。
func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
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

// RetryWithRetriesAndBackoff 用 cenkalti/backoff 做带 jitter 的指数退避重试。
//   - maxRetries < 0  ：无限重试（直到 ctx 取消）
//   - maxRetries == 0 ：仅尝试一次，不重试
//   - maxRetries > 0  ：最多重试 maxRetries 次（即总共最多 maxRetries+1 次尝试）
//
// initial / max 控制指数退避区间。退避带 ±50% jitter（RandomizationFactor=0.5），
// 防止多 Agent 同步重连造成的惊群。返回最后一次失败的错误。
func RetryWithRetriesAndBackoff(ctx context.Context, op func() error, initial, max time.Duration, maxRetries int, desc string) error {
	if initial <= 0 {
		initial = time.Second
	}
	if max <= 0 {
		max = 60 * time.Second
	}
	// 构造退避策略：先 MaxRetries 再 Context，backoff.getContext 能穿透两层包装提取 ctx
	var policy backoff.BackOff = newExponentialBackoff(initial, max)
	if maxRetries >= 0 {
		policy = backoff.WithMaxRetries(policy, uint64(maxRetries))
	}
	policy = backoff.WithContext(policy, ctx)

	err := backoff.Retry(op, policy)
	if err == nil {
		return nil
	}
	// ctx 取消时 backoff.Retry 返回 ctx.Err()，直接透传（保留 %w 让调用方 errors.Is 判别）
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// 正常耗尽重试次数，加 desc 前缀便于排查
	return fmt.Errorf("%s: 已达最大重试次数 %d: %w", desc, maxRetries, err)
}
