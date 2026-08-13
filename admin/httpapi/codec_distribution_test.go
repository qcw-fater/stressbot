// Package admin 覆盖多 codec 上传、基线与浏览器下载测试。
//
// 覆盖：
//   - multipart 上传多份 adapter/*_codec.json + adapter/errors.json → TaskConfig.Codecs 含各文件、ErrorMap 非空。
//   - writeBaselineFiles 落盘各 *_codec.json + errors.json；baseline HTTP 可读各文件。
//   - handleGetTaskConfig 下载端点能取到多个 adapter/*_codec.json 与 adapter/errors.json。
//
// 测试输入字节使用 T1.6 产物 conf/adapter/{tcp_logic,tcp_battle,udp_battle}_codec.json + errors.json。
package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	admintask "stressbot/admin/task"
	json "stressbot/internal/jsonx"
	"stressbot/internal/stresslog"

	"go.uber.org/zap"
)

// codecDistTestFiles 是 T1.6 产物的相对路径，用作上传/落盘测试输入字节。
var codecDistTestFiles = []string{
	"conf/adapter/tcp_logic_codec.json",
	"conf/adapter/tcp_battle_codec.json",
	"conf/adapter/udp_battle_codec.json",
	"conf/adapter/errors.json",
}

// codecDistRepoRoot 在测试初始化（chdir 前）解析得到的仓库根绝对路径，用于后续
// 在临时目录中也能读到 T1.6 产物 conf/adapter/*.json。
var codecDistRepoRoot = func() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	// 测试 cwd 为 admin/httpapi，仓库根 = 上两级。
	return filepath.Clean(filepath.Join(cwd, "..", ".."))
}()

// readCodecDistFile 读取仓库根下 rel 指向的测试输入文件（用绝对路径，不受 chdir 影响）。
func readCodecDistFile(t *testing.T, rel string) []byte {
	t.Helper()
	p := filepath.Join(codecDistRepoRoot, filepath.FromSlash(rel))
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取测试输入文件失败: %s: %v", p, err)
	}
	return data
}

// newCodecDistMultipart 构造 multipart body：
//   - flow.json（最小合法结构）
//   - adapter/<每个 *_codec.json>（按文件名作为 form field）
//   - adapter/errors.json
//
// 返回 body 与 content-type。
func newCodecDistMultipart(t *testing.T) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// 必需的表单字段
	mustField := func(name, value string) {
		if err := mw.WriteField(name, value); err != nil {
			t.Fatalf("write field %s: %v", name, err)
		}
	}
	mustField("name", "codec-dist-test")
	mustField("totalBots", "1")

	// flow.json（必须以 file 形式上传，handler 用 r.FormFile 取）
	hFlow := make(textproto.MIMEHeader)
	hFlow.Set("Content-Disposition", `form-data; name="flow.json"; filename="flow.json"`)
	hFlow.Set("Content-Type", "application/json")
	partFlow, err := mw.CreatePart(hFlow)
	if err != nil {
		t.Fatalf("create flow.json part: %v", err)
	}
	if _, err := partFlow.Write([]byte(`{"nodes":{},"actions":{}}`)); err != nil {
		t.Fatalf("write flow.json: %v", err)
	}

	// 多个 codec 文件 + errors.json，field 名为 adapter/<basename>
	writeFileField := func(fieldName, rel string) {
		data := readCodecDistFile(t, rel)
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="`+fieldName+`"; filename="`+filepath.Base(rel)+`"`)
		h.Set("Content-Type", "application/json")
		part, err := mw.CreatePart(h)
		if err != nil {
			t.Fatalf("create part %s: %v", fieldName, err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatalf("write part %s: %v", fieldName, err)
		}
	}
	writeFileField("adapter/tcp_logic_codec.json", "conf/adapter/tcp_logic_codec.json")
	writeFileField("adapter/tcp_battle_codec.json", "conf/adapter/tcp_battle_codec.json")
	writeFileField("adapter/udp_battle_codec.json", "conf/adapter/udp_battle_codec.json")
	writeFileField("adapter/errors.json", "conf/adapter/errors.json")

	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// setupCodecDistServer 构造一个最小 AdminServer（仅 TaskStore），并将 cwd 切到临时目录，
// 使 writeBaselineFiles 落盘的 conf/* 位于隔离目录。同时把全局 logger 替换为 nop logger，
// 避免未初始化 zap 时 handler 中的日志调用 panic。返回 server、临时目录、还原函数。
func setupCodecDistServer(t *testing.T) (*Handler, string, func()) {
	t.Helper()
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getcwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir tempdir: %v", err)
	}
	ts, err := NewTaskStore(filepath.Join(dir, "data"))
	if err != nil {
		_ = os.Chdir(origCwd)
		t.Fatalf("NewTaskStore: %v", err)
	}
	origLogger := stresslog.GetLogger()
	stresslog.ReplaceLogger(zap.NewNop())
	srv := &Handler{tasks: ts, nextID: testNextID}
	cleanup := func() {
		_ = os.Chdir(origCwd)
		if origLogger != nil {
			stresslog.ReplaceLogger(origLogger)
		}
	}
	return srv, dir, cleanup
}

// TestCodecDist_UploadPopulatesMultiCodec 上传多份 codec 文件后，TaskConfig.Codecs
// 含每个 *_codec.json，且 ErrorMap 非空。
func TestCodecDist_UploadPopulatesMultiCodec(t *testing.T) {
	srv, _, cleanup := setupCodecDistServer(t)
	defer cleanup()

	body, ct := newCodecDistMultipart(t)
	req := httptest.NewRequest(http.MethodPost, "/sbot/tasks", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	srv.handleCreateTask(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	task, ok := srv.tasks.Get(resp.ID)
	if !ok {
		t.Fatalf("task %s not found", resp.ID)
	}

	// Codecs 必须含 3 个 *_codec.json
	want := []string{"tcp_logic_codec.json", "tcp_battle_codec.json", "udp_battle_codec.json"}
	if len(task.Config.Codecs) != len(want) {
		t.Fatalf("Codecs len = %d, want %d (entries=%v)", len(task.Config.Codecs), len(want), task.Config.Codecs)
	}
	for _, w := range want {
		data, ok := task.Config.Codecs[w]
		if !ok {
			t.Fatalf("Codecs missing %s", w)
		}
		if len(data) == 0 {
			t.Fatalf("Codecs[%s] empty", w)
		}
	}
	// ErrorMap 必须非空（errors.json 内容）
	if len(task.Config.ErrorMap) == 0 {
		t.Fatalf("ErrorMap empty")
	}
}

// expectedCodecConfigFiles 返回浏览器配置下载应提供的 adapter 条目。
func expectedCodecConfigFiles() []string {
	return []string{
		"adapter/tcp_logic_codec.json",
		"adapter/tcp_battle_codec.json",
		"adapter/udp_battle_codec.json",
		"adapter/errors.json",
	}
}

// TestCodecDist_BaselineWriteAndReadRoundTrip writeBaselineFiles 落盘后磁盘有各 *_codec.json +
// errors.json；baseline HTTP 端点能读到这些文件。
func TestCodecDist_BaselineWriteAndReadRoundTrip(t *testing.T) {
	srv, dir, cleanup := setupCodecDistServer(t)
	defer cleanup()

	// 直接构造一个含多 codec 的 TaskConfig，落盘
	cfg := &admintask.TaskConfig{
		FlowJSON: json.RawMessage(`{}`),
		Codecs:   map[string][]byte{},
	}
	for _, rel := range []string{
		"conf/adapter/tcp_logic_codec.json",
		"conf/adapter/tcp_battle_codec.json",
		"conf/adapter/udp_battle_codec.json",
	} {
		cfg.Codecs[filepath.Base(rel)] = readCodecDistFile(t, rel)
	}
	cfg.ErrorMap = readCodecDistFile(t, "conf/adapter/errors.json")

	srv.writeBaselineFiles(cfg, []byte(`{}`))

	// 落盘校验
	for _, name := range []string{"tcp_logic_codec.json", "tcp_battle_codec.json", "udp_battle_codec.json", "errors.json"} {
		p := filepath.Join(dir, "conf/adapter", name)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("baseline file %s not written: %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("baseline file %s empty", name)
		}
	}
	// 不应有 codec.lua / error.lua
	for _, name := range []string{"codec.lua", "error.lua"} {
		p := filepath.Join(dir, "conf/adapter", name)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("legacy baseline file %s should not be written", name)
		}
	}

	// baseline HTTP：通过 serveBaselineFile 模式读取（adapter 目录按文件名透传）
	for _, name := range []string{"tcp_logic_codec.json", "tcp_battle_codec.json", "udp_battle_codec.json", "errors.json"} {
		req := httptest.NewRequest(http.MethodGet, "/sbot/baseline/adapter/"+name, nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		srv.handleBaselineCodecFile(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("baseline GET %s: expected 200, got %d", name, rec.Code)
		}
		if len(rec.Body.Bytes()) == 0 {
			t.Fatalf("baseline GET %s: empty body", name)
		}
	}
}

// TestCodecDist_DownloadServesMultiFiles handleGetTaskConfig 能取到多个 adapter/*_codec.json
// 与 adapter/errors.json。
func TestCodecDist_DownloadServesMultiFiles(t *testing.T) {
	srv, _, cleanup := setupCodecDistServer(t)
	defer cleanup()

	cfg := &admintask.TaskConfig{
		FlowJSON: json.RawMessage(`{}`),
		Codecs:   map[string][]byte{},
	}
	for _, rel := range []string{
		"conf/adapter/tcp_logic_codec.json",
		"conf/adapter/tcp_battle_codec.json",
		"conf/adapter/udp_battle_codec.json",
	} {
		cfg.Codecs[filepath.Base(rel)] = readCodecDistFile(t, rel)
	}
	cfg.ErrorMap = readCodecDistFile(t, "conf/adapter/errors.json")

	task := &admintask.Task{
		ID:        "t-download",
		Name:      "download-test",
		State:     admintask.TaskPending,
		TotalBots: 1,
		Config:    *cfg,
	}
	if err := srv.tasks.Create(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	downloadCases := expectedCodecConfigFiles()
	for _, p := range downloadCases {
		req := httptest.NewRequest(http.MethodGet, "/sbot/tasks/"+task.ID+"/config/"+p, nil)
		req.SetPathValue("id", task.ID)
		req.SetPathValue("path", p)
		rec := httptest.NewRecorder()
		srv.handleGetTaskConfig(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("download %s: expected 200, got %d, body=%s", p, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if len(body) == 0 || strings.TrimSpace(body) == "" {
			t.Fatalf("download %s: empty body", p)
		}
		// 下载内容应以 JSON 对象/数组起始（{ 或 [），errors.json 是对象
		first := strings.TrimSpace(body)[0]
		if first != '{' && first != '[' {
			t.Fatalf("download %s: expected JSON, got prefix %q", p, first)
		}
	}
}
