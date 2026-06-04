package admin

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

func normalizeAddr(addr string) string {
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	return addr
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
	defer resp.Body.Close()
	var result struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Version, nil
}

func (d *AgentDispatcher) post(addr, path string, body any, retries int) error {
	backoff := 1 * time.Second
	url := fmt.Sprintf("http://%s%s", normalizeAddr(addr), path)

	for i := 0; i <= retries; i++ {
		var bodyReader io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				return fmt.Errorf("marshal body: %w", err)
			}
			bodyReader = bytes.NewReader(data)
		}

		req, err := http.NewRequest("POST", url, bodyReader)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := d.httpClient.Do(req)
		if err != nil {
			if i == retries {
				return fmt.Errorf("after %d retries: %w", retries, err)
			}
			stresslog.Warn("[DISPATCHER] POST 失败，将重试",
				zap.String("url", url),
				zap.Int("attempt", i+1),
				zap.Error(err))
			time.Sleep(backoff)
			backoff = backoff * 2
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if i == retries {
			return fmt.Errorf("agent returned status %d", resp.StatusCode)
		}
		stresslog.Warn("[DISPATCHER] POST 失败，将重试",
			zap.String("url", url),
			zap.Int("attempt", i+1),
			zap.Int("status", resp.StatusCode))
		time.Sleep(backoff)
		backoff = backoff * 2
		if backoff > 10*time.Second {
			backoff = 10 * time.Second
		}
	}
	return fmt.Errorf("unreachable")
}

func (d *AgentDispatcher) get(addr, path string) (*http.Response, error) {
	url := fmt.Sprintf("http://%s%s", normalizeAddr(addr), path)
	return d.httpClient.Get(url)
}
