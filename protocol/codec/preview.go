// Package codec — Preview helper（T1.7 第 4 项）。
//
// Preview 是给 T4 Admin 的 `POST /sbot/codec/preview` 端点使用的纯计算 helper：
// 接收一份 codec schema + 单次 encode/decode 入参，返回帧/字段解释，便于前端在不
// 发起真实网络任务的前提下预览 schema 的编解码效果。
//
// 设计约束（与 T1.7 brief 第 4 项一致）：
//   - **纯 Go + codec 包**：不 import gopher-lua；不读写文件、不接网络、不依赖任务状态。
//   - **不 panic**：schema 编译失败、参数非法（坏 hex、未知 mode/transport）一律填
//     `PreviewResult.Error`（中文）返回，由调用方决定如何呈现给用户。
//   - **transport 入参保留但当前不影响单 codec 计算**：codec 单 transport，encrypt offset
//     已在 schema 里固化；保留 transport 仅为 T3/T4 语义清晰（未来若 codec 多 transport
//     再接线）。
//   - route 字段值支持 int/float/string（数值化：math.Floor 截断取整）。
package codec

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
)

// PreviewField 描述 header 中一个字段的解释结果（供前端逐字段展示）。
type PreviewField struct {
	Name   string `json:"name"`
	Value  uint64 `json:"value"`  // 数值化（按字段 size 与 endian/kind 读取）
	Offset int    `json:"offset"` // 在 header 中的字节偏移
	Size   int    `json:"size"`   // 字段字节数
}

// PreviewResult 是 Preview 的返回。Error 非空时其它字段为零值。
type PreviewResult struct {
	Mode      string         `json:"mode"`                // "encode"|"decode"
	FrameHex  string         `json:"frameHex,omitempty"`  // encode 出参：完整帧 hex
	BodyHex   string         `json:"bodyHex,omitempty"`   // decode 出参：解出 body hex
	RouteKey  string         `json:"routeKey,omitempty"`  // decode 出参：routeKey
	HeaderErr uint64         `json:"headerErr,omitempty"` // decode 出参：头 errorCode
	Fields    []PreviewField `json:"fields,omitempty"`    // header 字段逐项解释
	Error     string         `json:"error,omitempty"`     // schema 编译/运行错误（中文）
}

// Preview 编译 schema 并执行单次 encode 或 decode，返回帧/字段解释。
//
// 入参：
//   - schema：*Schema（调用方应已 LoadSchema 得到；Preview 内部再次 Validate+编译）。
//   - mode："encode" | "decode"。
//   - transport："tcp" | "udp"（当前不影响单 codec 计算；保留入参为 T3/T4 语义清晰）。
//   - route：route 字段 map（key 为字段名，value 支持 int/float/string，数值化取整）。
//     encode 必填；decode 忽略（route 来自头）。
//   - bodyHex：encode 入参 body 的 hex；decode 忽略。
//   - keyHex：secretKey 的 hex（可为空 → 不传 key）。
//   - frameHex：decode 入参完整帧的 hex；encode 忽略。
//
// 失败语义：schema==nil / 编译失败 / 坏 hex / 未知 mode / 未知 transport → 填 Error 返回，
// 不 panic。合法 encode/decode 返回 FrameHex/BodyHex + Fields。
func Preview(schema *Schema, mode, transport string, route map[string]any, bodyHex, keyHex, frameHex string) PreviewResult {
	res := PreviewResult{Mode: mode}

	// ---- 1. transport 校验（保留语义；当前不影响计算）----
	switch transport {
	case "tcp", "udp", "":
		// 合法（空串视为 tcp，向前兼容）
	default:
		res.Error = fmt.Sprintf("不支持的协议方向 %q（仅支持 tcp/udp）", transport)
		return res
	}

	// ---- 2. schema 编译 ----
	if schema == nil {
		res.Error = "schema 为空"
		return res
	}
	c, err := NewSchemaCodec(schema, nil)
	if err != nil {
		res.Error = fmt.Sprintf("schema 编译失败：%v", err)
		return res
	}

	// ---- 3. 解析 keyHex（encode/decode 共用）----
	key, err := decodeHexOrError(keyHex)
	if err != nil {
		res.Error = fmt.Sprintf("keyHex 非法：%v", err)
		return res
	}

	// ---- 4. 分派 ----
	switch mode {
	case "encode":
		return previewEncode(res, c, schema, route, bodyHex, key)
	case "decode":
		return previewDecode(res, c, schema, frameHex, key)
	default:
		res.Error = fmt.Sprintf("不支持的 mode %q（仅支持 encode/decode）", mode)
		return res
	}
}

// previewEncode 执行 encode 并填 FrameHex + Fields。
func previewEncode(res PreviewResult, c *SchemaCodec, schema *Schema, route map[string]any, bodyHex string, key []byte) PreviewResult {
	body, err := decodeHexOrError(bodyHex)
	if err != nil {
		res.Error = fmt.Sprintf("bodyHex 非法：%v", err)
		return res
	}
	// route 字段值数值化（支持 int/float/string；string 经 routePreviewFloorInt 转整数）。
	normalized := normalizeRouteMap(route)
	// codec 单 transport；EncodeTCP/EncodeUDP 同管线（offset 已固化在 schema）。
	// 这里走 EncodeTCP（transport 入参当前不影响计算，见 Preview godoc）。
	frame := c.EncodeTCP(normalized, body, key)
	res.FrameHex = hex.EncodeToString(frame)
	headerSize := c.HeaderSize()
	if len(frame) >= headerSize {
		res.Fields = extractFields(schema, frame[:headerSize])
	}
	return res
}

// normalizeRouteMap 把 route map 中 string/float 值规约为 int64（EncodeTCP 的
// routeMapFloorInt 仅识别数值类型；string 在此先转 int）。
func normalizeRouteMap(route map[string]any) map[string]any {
	if route == nil {
		return nil
	}
	out := make(map[string]any, len(route))
	for k, v := range route {
		if s, ok := v.(string); ok {
			out[k] = routePreviewFloorInt(s)
			continue
		}
		out[k] = v
	}
	return out
}

// previewDecode 执行 decode 并填 BodyHex + RouteKey + HeaderErr + Fields。
func previewDecode(res PreviewResult, c *SchemaCodec, schema *Schema, frameHex string, key []byte) PreviewResult {
	frame, err := decodeHexOrError(frameHex)
	if err != nil {
		res.Error = fmt.Sprintf("frameHex 非法：%v", err)
		return res
	}
	routeKey, body, headerErr := c.DecodeTCP(frame, key)
	res.RouteKey = routeKey
	res.HeaderErr = headerErr
	if body != nil {
		res.BodyHex = hex.EncodeToString(body)
	}
	headerSize := c.HeaderSize()
	if len(frame) >= headerSize {
		res.Fields = extractFields(schema, frame[:headerSize])
	}
	return res
}

// extractFields 按 schema.Header 的 (name, offset, size, type, endian) 从 header 字节
// 读出每个字段的数值化值。endian 缺省回退 schema.EndianDefault（le/be）。
func extractFields(schema *Schema, header []byte) []PreviewField {
	if schema == nil || len(schema.Header) == 0 || len(header) == 0 {
		return nil
	}
	defaultEndian := resolveEndianOrDefault(schema.EndianDefault)
	out := make([]PreviewField, 0, len(schema.Header))
	for _, f := range schema.Header {
		end := f.Offset + f.Size
		pf := PreviewField{Name: f.Name, Offset: f.Offset, Size: f.Size}
		if end <= len(header) {
			order := defaultEndian
			switch f.Endian {
			case "be":
				order = binary.BigEndian
			case "le":
				order = binary.LittleEndian
			}
			pf.Value = readFieldU64(header[f.Offset:end], order, f.Type)
		}
		out = append(out, pf)
	}
	return out
}

// resolveEndianOrDefault 把 schema.EndianDefault 字符串解析为 binary.ByteOrder，
// 非法或缺省回退 LittleEndian（与 compile.go resolveEndian 行为对齐；Preview 不复用
// 未导出函数以保持 preview.go 自包含可读）。
func resolveEndianOrDefault(s string) binary.ByteOrder {
	if s == "be" {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// readFieldU64 按 type 从 src（len==size）读无符号整数（preview 只需数值化展示）。
// 与 engine.go readUint 同口径；bytes 类型按小端拼接为 uint64（仅展示用）。
func readFieldU64(src []byte, order binary.ByteOrder, fieldType string) uint64 {
	switch fieldType {
	case "u8", "i8":
		if len(src) >= 1 {
			return uint64(src[0])
		}
	case "u16", "i16":
		if len(src) >= 2 {
			return uint64(order.Uint16(src))
		}
	case "u24", "i24":
		if len(src) >= 3 {
			var tmp [4]byte
			copy(tmp[:3], src[:3])
			if order == binary.BigEndian {
				return uint64(tmp[0])<<16 | uint64(tmp[1])<<8 | uint64(tmp[2])
			}
			return uint64(order.Uint32(tmp[:]))
		}
	case "u32", "i32":
		if len(src) >= 4 {
			return uint64(order.Uint32(src))
		}
	case "u64", "i64":
		if len(src) >= 8 {
			return order.Uint64(src)
		}
	case "f32":
		if len(src) >= 4 {
			return uint64(order.Uint32(src)) // 数值化展示原始位
		}
	case "f64":
		if len(src) >= 8 {
			return order.Uint64(src)
		}
	default:
		// bytes/未知：低字节小端拼接（展示用，与 engine.go readUint default 一致）。
		var v uint64
		for i := range src {
			v |= uint64(src[i]) << (uint(i) * 8)
		}
		return v
	}
	return 0
}

// decodeHexOrError 解析 hex 字符串为字节；空串返回 nil（不报错，表示「不传 key/body」）。
func decodeHexOrError(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// routePreviewFloorInt 是 Preview 路径上的 route 字段数值化（与 engine routeMapFloorInt
// 同口径，但对 string 提供（JSON 反序列化的 route 值可能为 string））。
//
// 保留供未来 Preview 扩展（当前 EncodeTCP 内部已 routeMapFloorInt 处理 int/float；
// string 路由字段需先经此转 int 再传入，由调用方/T4 决定）。
func routePreviewFloorInt(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return int64(math.Floor(x))
	case float32:
		return int64(math.Floor(float64(x)))
	case int:
		return int64(x)
	case int64:
		return x
	case string:
		var n int64
		_, err := fmt.Sscanf(x, "%d", &n)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}
