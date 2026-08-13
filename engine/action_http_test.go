package engine

import (
	flowdef "stressbot/flow"
	"strings"
	"testing"

	"go.uber.org/zap"
	"stressbot/errcode"
	"stressbot/internal/stresslog"
	"stressbot/state"
)

// httpStatusNetSender 注入指定 StatusCode + Body 的 HTTP 响应，复用 fakeNetSender
// 兜底其余方法（TCP/UDP/connect 不参与本测试）。
type httpStatusNetSender struct {
	*fakeNetSender
	statusCode int
	body       []byte
	httpReq    int32
}

func (f *httpStatusNetSender) HTTPRequest(string, string, string, []byte) (*HTTPExchange, error) {
	f.httpReq++
	return &HTTPExchange{StatusCode: f.statusCode, Body: f.body}, nil
}

// TestHTTPRequest_Non2xx_DetailIncludesStatusAndBody 声明式 httpRequest 收到非 2xx 响应时，
// 返回的 *ActionError：
//   - Code == ErrHTTPStatus(48)
//   - Detail 含 status=<code> 与状态文本（如 "Not Found"）
//   - Detail **不含** 响应 body 内容（body 应进日志而非 detail）
//
// TDD：先 FAIL（当前 detail 仅 "action=X statusCode=Y"，无状态文本）。
func TestHTTPRequest_Non2xx_DetailIncludesStatusAndBody(t *testing.T) {
	// 非 2xx 分支会写 warn 日志，需初始化 logger（用 noop 避免输出噪音）。
	stresslog.ReplaceLogger(zap.NewNop())

	const actionName = "createTeam"
	body := []byte(`{"error":1004,"errstr":"队伍已满"}`)
	fake := &httpStatusNetSender{
		fakeNetSender: &fakeNetSender{},
		statusCode:    404,
		body:          body,
	}
	ae := &ActionExecutor{netSender: fake, store: state.NewStore()}

	_, _, _, err := ae.execHTTPRequest(&flowdef.ActionDef{Name: actionName, URL: "http://x/y"})
	if err == nil {
		t.Fatal("非 2xx 应返回 error")
	}
	ae2, ok := err.(*errcode.ActionError)
	if !ok {
		t.Fatalf("应返回 *ActionError，实际 %T：%v", err, err)
	}
	if ae2.Code != errcode.ErrHTTPStatus {
		t.Errorf("Code 应为 ErrHTTPStatus(%d)，实际 %d", errcode.ErrHTTPStatus, ae2.Code)
	}

	detail := ae2.Detail
	if !strings.Contains(detail, "status=404") {
		t.Errorf("Detail 应含 \"status=404\"：%q", detail)
	}
	if !strings.Contains(detail, "Not Found") {
		t.Errorf("Detail 应含 HTTP 状态文本 \"Not Found\"：%q", detail)
	}
	if strings.Contains(detail, "队伍已满") {
		t.Errorf("Detail 不应含响应 body 内容：%q", detail)
	}
	if !strings.HasPrefix(detail, "action="+actionName) {
		t.Errorf("Detail 应以 action=%s 开头：%q", actionName, detail)
	}
}
