// Package admin — T4.2 codec preview/algorithms 端点测试。
//
// 覆盖（与 brief 验收逐条对齐）：
//   - preview encode：合法 schema + route + body + key → 200，FrameHex 非空、Fields 含 cmd/act。
//   - preview decode：用 encode 出的帧 hex → 200，BodyHex/RouteKey 还原。
//   - preview 畸形 schema（缺 length 字段）→ **200** + Error 非空（中文）。
//   - preview 请求体非法 JSON → 400。
//   - preview 请求体 schema 字段类型错（非对象）→ 400（schema 反序列化失败）。
//   - preview 未知 mode → 200 + Error。
//   - algorithms：200，返回清单含 xor_carry_rol/gzip/xor8，按 op 分组（cipher→compress→checksum→hash）。
//
// 测试策略：codec 端点为纯计算（不读 AdminServer 状态），用零值 *AdminServer +
// httptest.NewRecorder 直接驱动 handler，避免 NewAdminServer 的重依赖（TaskStore 落
// 盘、Redis 连通性校验）。
package admin

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"stressbot/codec"
)

// validCodecSchemaJSON 是 testdata/tcp_logic_codec.json 的等价 JSON（与 codec 包对拍
// 用例同 schema）。这里硬编码一份，使 admin 测试不依赖 codec/testdata 相对路径。
const validCodecSchemaJSON = `{
  "version": 1,
  "endianDefault": "le",
  "frame": {
    "headerSize": 12,
    "trailerSize": 0,
    "lengthIncludesHeader": false,
    "lengthIncludesTrailer": false
  },
  "header": [
    { "name": "bodyLen", "offset": 0,  "size": 4, "type": "u32", "endian": "le", "role": "length" },
    { "name": "errCode", "offset": 4,  "size": 2, "type": "u16", "role": "errorCode" },
    { "name": "cmd",     "offset": 6,  "size": 1, "type": "u8",  "role": "route" },
    { "name": "act",     "offset": 7,  "size": 1, "type": "u8",  "role": "route" },
    { "name": "index",   "offset": 8,  "size": 2, "type": "u16", "role": "value", "source": { "kind": "const", "value": 0 } },
    { "name": "flags",   "offset": 10, "size": 1, "type": "u8",  "role": "flags",
      "bits": [ { "name": "encrypted", "bit": 0 }, { "name": "compressed", "bit": 1 } ] },
    { "name": "bcc",     "offset": 11, "size": 1, "type": "u8",  "role": "checksumOut", "from": "enc.bcc" }
  ],
  "routeKeyTemplate": "{cmd}:{act}",
  "pipeline": [
    { "op": "compress", "name": "gz", "algo": "gzip", "flag": "compressed",
      "when": { "minBodyLen": 2048, "onlySmaller": true }, "onError": "fail" },
    { "op": "encrypt",  "name": "enc", "algo": "xor_carry_rol", "params": { "rol": 3 }, "keyLen": 32, "flag": "encrypted",
      "offset": { "encode": 0, "decode": 0 },
      "when": { "requireKey": true, "minBodyLen": 1, "guards": [ { "field": "cmd", "op": "neq", "value": 0 } ] },
      "produces": [ { "name": "bcc", "algo": "xor8", "region": "ciphered" } ],
      "onError": "fail" }
  ]
}`

// previewKeyHex 32B key 的 hex（与 codec/preview_test.go genKeyHex 同种子）。
const previewKeyHex = "01080f161d242b32394047505e656c737a81888f969da4abb2b9c0c7ced5dce3"

// doCodecPreview 调用 handleCodecPreview 并返回 (status, decoded PreviewResult, raw body)。
func doCodecPreview(t *testing.T, body any) (int, codec.PreviewResult, []byte) {
	t.Helper()
	s := &AdminServer{}
	var rdr bytes.Buffer
	if raw, ok := body.([]byte); ok {
		rdr = *bytes.NewBuffer(raw)
	} else {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		rdr = *bytes.NewBuffer(b)
	}
	req := httptest.NewRequest(http.MethodPost, "/sbot/codec/preview", &rdr)
	rec := httptest.NewRecorder()
	s.handleCodecPreview(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	var pr codec.PreviewResult
	_ = json.NewDecoder(resp.Body).Decode(&pr)
	return resp.StatusCode, pr, rec.Body.Bytes()
}

// findPreviewField 在 Fields 中按名查；缺失 fatal。
func findPreviewField(t *testing.T, fields []codec.PreviewField, name string) codec.PreviewField {
	t.Helper()
	for _, f := range fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("字段 %q 未在 Fields 中找到", name)
	return codec.PreviewField{}
}

func TestCodecPreview_Encode_OK(t *testing.T) {
	bodyHex := hex.EncodeToString([]byte("hello world payload"))
	req := map[string]any{
		"schema":    json.RawMessage(validCodecSchemaJSON),
		"mode":      "encode",
		"transport": "tcp",
		"route":     map[string]any{"cmd": float64(100), "act": float64(7)},
		"bodyHex":   bodyHex,
		"keyHex":    previewKeyHex,
	}
	status, pr, _ := doCodecPreview(t, req)
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200", status)
	}
	if pr.Error != "" {
		t.Fatalf("encode 不应有错：%q", pr.Error)
	}
	if pr.Mode != "encode" {
		t.Errorf("Mode=%q want encode", pr.Mode)
	}
	if pr.FrameHex == "" {
		t.Fatal("FrameHex 为空")
	}
	if len(pr.Fields) != 7 {
		t.Errorf("Fields 数=%d want 7", len(pr.Fields))
	}
	if got := findPreviewField(t, pr.Fields, "cmd"); got.Value != 100 {
		t.Errorf("cmd Value=%d want 100", got.Value)
	}
	if got := findPreviewField(t, pr.Fields, "act"); got.Value != 7 {
		t.Errorf("act Value=%d want 7", got.Value)
	}
}

func TestCodecPreview_EncodeDecode_Roundtrip(t *testing.T) {
	// 先 encode 拿到帧。
	bodyHex := hex.EncodeToString([]byte("round-trip-body-12345"))
	encReq := map[string]any{
		"schema":    json.RawMessage(validCodecSchemaJSON),
		"mode":      "encode",
		"transport": "tcp",
		"route":     map[string]any{"cmd": float64(100), "act": float64(7)},
		"bodyHex":   bodyHex,
		"keyHex":    previewKeyHex,
	}
	_, enc, _ := doCodecPreview(t, encReq)
	if enc.Error != "" {
		t.Fatalf("encode：%q", enc.Error)
	}

	// 用 enc.FrameHex 做 decode。
	decReq := map[string]any{
		"schema":    json.RawMessage(validCodecSchemaJSON),
		"mode":      "decode",
		"transport": "tcp",
		"keyHex":    previewKeyHex,
		"frameHex":  enc.FrameHex,
	}
	status, dec, _ := doCodecPreview(t, decReq)
	if status != http.StatusOK {
		t.Fatalf("decode status=%d want 200", status)
	}
	if dec.Error != "" {
		t.Fatalf("decode：%q", dec.Error)
	}
	if dec.RouteKey != "100:7" {
		t.Errorf("RouteKey=%q want 100:7", dec.RouteKey)
	}
	if dec.HeaderErr != 0 {
		t.Errorf("HeaderErr=%d want 0", dec.HeaderErr)
	}
	if dec.BodyHex != bodyHex {
		t.Errorf("BodyHex 往还原失败\n got=%s\n want=%s", dec.BodyHex, bodyHex)
	}
	if got := findPreviewField(t, dec.Fields, "cmd"); got.Value != 100 {
		t.Errorf("decode cmd Value=%d want 100", got.Value)
	}
}

func TestCodecPreview_BadSchema_OK_WithError(t *testing.T) {
	// 缺 role:"length" 字段的非法 schema —— 应返回 200 + 非空 Error（中文）。
	badSchema := `{
      "version": 1,
      "endianDefault": "le",
      "frame": { "headerSize": 4 },
      "header": [
        { "name": "cmd", "offset": 0, "size": 1, "type": "u8", "role": "route" }
      ],
      "routeKeyTemplate": "{cmd}"
    }`
	req := map[string]any{
		"schema":    json.RawMessage(badSchema),
		"mode":      "encode",
		"transport": "tcp",
		"route":     map[string]any{"cmd": float64(1)},
		"bodyHex":   "00",
	}
	status, pr, _ := doCodecPreview(t, req)
	// 畸形 schema → 200 + Error（编辑器预览语义，不是 HTTP 错误）。
	if status != http.StatusOK {
		t.Fatalf("畸形 schema 应返回 200，got status=%d", status)
	}
	if pr.Error == "" {
		t.Fatal("畸形 schema 应填 Error")
	}
	if !containsHan(pr.Error) {
		t.Errorf("畸形 schema Error 应为中文，got %q", pr.Error)
	}
}

func TestCodecPreview_UnknownMode_OK_WithError(t *testing.T) {
	req := map[string]any{
		"schema":    json.RawMessage(validCodecSchemaJSON),
		"mode":      "frobnicate",
		"transport": "tcp",
		"bodyHex":   "00",
	}
	status, pr, _ := doCodecPreview(t, req)
	if status != http.StatusOK {
		t.Fatalf("未知 mode 应返回 200，got status=%d", status)
	}
	if pr.Error == "" {
		t.Fatal("未知 mode 应填 Error")
	}
}

func TestCodecPreview_InvalidRequestBody_400(t *testing.T) {
	// 非法 JSON。
	status, _, raw := doCodecPreview(t, []byte(`{not json`))
	if status != http.StatusBadRequest {
		t.Errorf("非法 JSON 应返回 400，got status=%d (body=%s)", status, string(raw))
	}
}

func TestCodecPreview_SchemaNotObject_400(t *testing.T) {
	// schema 字段是字符串而非对象 → 反序列化 CodecSchema 失败 → 400。
	req := map[string]any{
		"schema":    "not-an-object",
		"mode":      "encode",
		"transport": "tcp",
		"bodyHex":   "00",
	}
	status, _, raw := doCodecPreview(t, req)
	if status != http.StatusBadRequest {
		t.Errorf("schema 非对象应返回 400，got status=%d (body=%s)", status, string(raw))
	}
}

// ---------------------------------------------------------------------------
// algorithms
// ---------------------------------------------------------------------------

func TestCodecAlgorithms_OK(t *testing.T) {
	s := &AdminServer{}
	req := httptest.NewRequest(http.MethodGet, "/sbot/codec/algorithms", nil)
	rec := httptest.NewRecorder()
	s.handleCodecAlgorithms(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var metas []codec.AlgoMeta
	if err := json.NewDecoder(resp.Body).Decode(&metas); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(metas) == 0 {
		t.Fatal("algorithms 返回空")
	}
	// 按名收集，校验关键算法存在。
	byName := map[string]codec.AlgoMeta{}
	for _, m := range metas {
		byName[m.Name] = m
	}
	for _, want := range []string{"xor_carry_rol", "gzip", "xor8"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("algorithms 缺少 %q（返回清单：%v）", want, algoNames(metas))
		}
	}
	// op 分组校验：cipher → compress → checksum → hash 顺序，组内不要求严格字母序由 codec
	// 包保证；这里只校验整体单调（同 op 连续）。
	opOrder := []string{}
	for _, m := range metas {
		if len(opOrder) == 0 || opOrder[len(opOrder)-1] != m.Op {
			opOrder = append(opOrder, m.Op)
		}
	}
	want := []string{"cipher", "compress", "checksum", "hash"}
	if len(opOrder) != len(want) {
		t.Errorf("op 分组数=%d want %d（实际顺序 %v）", len(opOrder), len(want), opOrder)
	} else {
		for i := range want {
			if opOrder[i] != want[i] {
				t.Errorf("op 分组顺序[%d]=%q want %q（实际 %v）", i, opOrder[i], want[i], opOrder)
				break
			}
		}
	}
}

func TestCodecAlgorithms_RouteRegistered(t *testing.T) {
	// 端点必须注册到 /sbot/ 路由表。用零值 AdminServer 取 mux 后发请求——未注册的
	// /sbot/codec/algorithms 会落到静态文件 fallback 返回 404，命中 handler 才是 200。
	s := &AdminServer{}
	mux := s.registerRoutes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sbot/codec/algorithms", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/sbot/codec/algorithms 命中后 status=%d want 200（未注册？）", rec.Code)
	}
}

func TestCodecPreview_RouteRegistered(t *testing.T) {
	s := &AdminServer{}
	mux := s.registerRoutes()
	rec := httptest.NewRecorder()
	// 用合法请求体确保命中 handler（不返回 404 即证明已注册）。
	body := bytes.NewBufferString(`{"schema":{"version":1},"mode":"encode","transport":"tcp","bodyHex":"00"}`)
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sbot/codec/preview", body))
	// 不应 404（静态 fallback 行为）；schema 不完整会 200+Error 或被 handler 接管。
	if rec.Code == http.StatusNotFound {
		t.Fatal("/sbot/codec/preview 未注册到路由表（404）")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func algoNames(metas []codec.AlgoMeta) []string {
	out := make([]string, 0, len(metas))
	for _, m := range metas {
		out = append(out, m.Name)
	}
	return out
}

// containsHan 粗判字符串是否含汉字（Unicode Han）。
func containsHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}
