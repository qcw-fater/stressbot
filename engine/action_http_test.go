package engine

import (
	"errors"
	"stressbot/binding"
	flowdef "stressbot/flow"
	"strings"
	"testing"

	"stressbot/errcode"
	json "stressbot/internal/jsonx"
	"stressbot/internal/stresslog"
	"stressbot/state"

	"go.uber.org/zap"
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

// bodyCapturingNetSender 记录最后一次 HTTP 请求的 contentType 与 body，用于校验请求体编码。
type bodyCapturingNetSender struct {
	*fakeNetSender
	gotContentType string
	gotBody        []byte
}

func (f *bodyCapturingNetSender) HTTPRequest(_, _, contentType string, body []byte) (*HTTPExchange, error) {
	f.gotContentType = contentType
	f.gotBody = body
	return &HTTPExchange{StatusCode: 200}, nil
}

// httpRequest 的 JSON body 必须带上 map 绑定：resolveFieldValue 不认 map，
// 早期实现让 map 字段整块从请求体里消失，服务端只会看到缺参。
func TestHTTPRequest_JSONBodyIncludesMapBinding(t *testing.T) {
	stresslog.ReplaceLogger(zap.NewNop())

	fake := &bodyCapturingNetSender{fakeNetSender: &fakeNetSender{}}
	ae := &ActionExecutor{netSender: fake, store: state.NewStore()}

	_, _, _, err := ae.execHTTPRequest(t.Context(), &flowdef.ActionDef{
		Name: "report",
		URL:  "http://x/y",
		Bindings: []binding.FieldBind{{
			Field: "params",
			Type:  binding.BindMap,
			Entries: []binding.MapEntryBind{
				{Key: "kind", Value: binding.FieldBind{Type: binding.BindFixed, Value: "guild"}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("含 map 绑定的 httpRequest 不应失败: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(fake.gotBody, &decoded); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v（body=%s）", err, fake.gotBody)
	}
	params, ok := decoded["params"].(map[string]any)
	if !ok {
		t.Fatalf("请求体缺少 params map：%s", fake.gotBody)
	}
	if params["kind"] != "guild" {
		t.Errorf(`params["kind"] = %#v, want "guild"`, params["kind"])
	}
}

// form 是扁平的 k=v 编码，map 绑定没有无歧义表示，必须报错而不是发出 "map[k:v]" 串。
func TestHTTPRequest_FormRejectsMapBinding(t *testing.T) {
	stresslog.ReplaceLogger(zap.NewNop())

	fake := &bodyCapturingNetSender{fakeNetSender: &fakeNetSender{}}
	ae := &ActionExecutor{netSender: fake, store: state.NewStore()}

	_, _, _, err := ae.execHTTPRequest(t.Context(), &flowdef.ActionDef{
		Name:        "report",
		URL:         "http://x/y",
		ContentType: flowdef.ContentForm,
		Bindings: []binding.FieldBind{{
			Field:   "params",
			Type:    binding.BindMap,
			Entries: []binding.MapEntryBind{{Key: "kind", Value: binding.FieldBind{Type: binding.BindFixed, Value: "guild"}}},
		}},
	})
	if err == nil {
		t.Fatal("form + map 绑定应报错")
	}
	actionErr, ok := errors.AsType[*errcode.ActionError](err)
	if !ok || actionErr.Code != errcode.ErrMarshalBody {
		t.Fatalf("err=%v，期望 ErrMarshalBody", err)
	}
	if !strings.Contains(actionErr.Detail, "不支持 map 绑定") {
		t.Errorf("detail=%q 应说明 form 不支持 map 绑定", actionErr.Detail)
	}
	if fake.gotBody != nil {
		t.Error("配置非法时不应发出请求")
	}
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

	_, _, _, err := ae.execHTTPRequest(t.Context(), &flowdef.ActionDef{Name: actionName, URL: "http://x/y"})
	if err == nil {
		t.Fatal("非 2xx 应返回 error")
	}
	ae2, ok := errors.AsType[*errcode.ActionError](err)
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
