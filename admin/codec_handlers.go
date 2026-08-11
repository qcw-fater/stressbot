// Package admin — T4.2 codec 预览/算法元数据端点。
//
// 这两个端点供前端 codec 编辑器（T3）调用：
//   - POST /sbot/codec/preview  包 codec.Preview：跑一次 encode/decode，返回帧/字段解释。
//   - GET  /sbot/codec/algorithms 包 codec.Algorithms：返回算法下拉元数据清单。
//
// 设计约束（与 t4-2-brief 逐条对齐）：
//   - **纯计算**：handler 内只做「解析请求 → 调 codec → 返回 JSON」，不读 conf/adapter、
//     不入库、不下发任务、不依赖 AdminServer 的任何运行态字段（tasks/agents/history...）。
//     schema 来自请求体（用户正在编辑的 codec 配置），而非磁盘文件。
//   - **preview 返回 HTTP 200 即使 PreviewResult.Error 非空**：这是编辑器预览语义，
//     前端据 Error 字段展示提示，不是 HTTP 错误。仅当请求 JSON 本身非法（unmarshal 失败）
//     或 schema 字段反序列化为 CodecSchema 失败时才返回 400。
//   - 遵循现有 admin handler 模式（方法签名 http.HandlerFunc、json 解码、writeJSON/writeError）。
package admin

import (
	"net/http"

	"stressbot/codec"
	configschema "stressbot/schema"
	json "stressbot/utils/jsonx"
)

// codecPreviewRequest 是 POST /sbot/codec/preview 的请求体。
//
// schema 用 json.RawMessage 承载，分两步反序列化：先解出整张请求体，再把 schema 原文
// unmarshal 成 *codec.CodecSchema。这样 schema 解析失败能单独返回 400（坏 schema），与
// 请求体整体非法 JSON 区分清晰。
type codecPreviewRequest struct {
	Schema    json.RawMessage `json:"schema"`              // 完整 codec.json 内容（对象形式）
	Mode      string          `json:"mode"`                // "encode" | "decode"
	Transport string          `json:"transport,omitempty"` // "tcp" | "udp"
	Route     map[string]any  `json:"route,omitempty"`     // encode 入参：route 字段 map
	BodyHex   string          `json:"bodyHex,omitempty"`   // encode 入参：body hex
	KeyHex    string          `json:"keyHex,omitempty"`    // encode/decode 入参：secretKey hex
	FrameHex  string          `json:"frameHex,omitempty"`  // decode 入参：完整帧 hex
}

// handleCodecPreview 处理 POST /sbot/codec/preview。
//
// 处理流程：
//  1. 解析请求体；非法 JSON → 400。
//  2. schema 字段反序列化为 *codec.CodecSchema；失败 → 400（schema 结构非法）。
//  3. 调 codec.Preview（纯计算，失败填 Error 不 panic）。
//  4. 返回 PreviewResult JSON（**HTTP 200 即使 Error 非空**）。
//
// 端点不访问 AdminServer 的任何运行态字段，handler 是 *AdminServer 方法仅为遵循现有
// admin handler 注册约定。
func (s *AdminServer) handleCodecPreview(w http.ResponseWriter, r *http.Request) {
	var req codecPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("invalid json: "+err.Error()))
		return
	}

	// schema 为空 → 400（编辑器至少需提供 schema 对象）。
	if len(req.Schema) == 0 {
		writeError(w, ErrInvalidArgument.WithMessage("schema required"))
		return
	}
	if err := configschema.ValidateCodec(req.Schema); err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("invalid schema: "+err.Error()))
		return
	}

	var schema codec.CodecSchema
	if err := json.Unmarshal(req.Schema, &schema); err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("invalid schema: "+err.Error()))
		return
	}

	res := codec.Preview(&schema, req.Mode, req.Transport, req.Route, req.BodyHex, req.KeyHex, req.FrameHex)
	// 编辑器预览语义：即使 res.Error 非空也返回 200，由前端据 Error 字段提示用户。
	writeJSON(w, http.StatusOK, res)
}

// handleCodecAlgorithms 处理 GET /sbot/codec/algorithms。
//
// 直接返回 codec.Algorithms()（纯计算，按 op 分组稳定排序：cipher→compress→checksum→hash）。
// 不接受入参（query 忽略）。
func (s *AdminServer) handleCodecAlgorithms(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, codec.Algorithms())
}
