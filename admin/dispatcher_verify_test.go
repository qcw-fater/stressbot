package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	stresslog "stressbot/utils/log"
)

// replaceLogger 在测试期间替换全局 logger 为 nop，避免 notify 回调里的日志 panic。
func replaceLogger(t *testing.T) {
	t.Helper()
	orig := stresslog.GetLogger()
	stresslog.ReplaceLogger(zap.NewNop())
	t.Cleanup(func() {
		if orig != nil {
			stresslog.ReplaceLogger(orig)
		}
	})
}

// 验证 4xx 永久错误不重试（立即停止）
func TestDispatcherPost4xxNoRetry(t *testing.T) {
	replaceLogger(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest) // 400 永久错误
	}))
	defer srv.Close()

	d := NewAgentDispatcher()
	err := d.post(srv.URL, "/test", map[string]string{"k": "v"}, 3)
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("意外错误: %v", err)
	}
	if calls != 1 {
		t.Fatalf("4xx 应立即停止不重试，期望 1 次调用，实际 %d 次", calls)
	}
}

// 验证 5xx 临时错误会重试
func TestDispatcherPost5xxRetries(t *testing.T) {
	replaceLogger(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError) // 500 临时错误
	}))
	defer srv.Close()

	d := NewAgentDispatcher()
	err := d.post(srv.URL, "/test", nil, 2)
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("意外错误: %v", err)
	}
	// retries=2 意味着总共 3 次尝试
	if calls != 3 {
		t.Fatalf("5xx 应重试，期望 3 次调用，实际 %d 次", calls)
	}
}

// 验证 2xx 成功立即返回
func TestDispatcherPost2xxSuccess(t *testing.T) {
	replaceLogger(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewAgentDispatcher()
	err := d.post(srv.URL, "/test", nil, 3)
	if err != nil {
		t.Fatalf("意外错误: %v", err)
	}
	if calls != 1 {
		t.Fatalf("成功应立即返回，期望 1 次调用，实际 %d 次", calls)
	}
}
