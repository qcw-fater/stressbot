// Package codec 提供声明式 codec 的 encode/decode 执行层。
//
// 本文件在编译产物 *SchemaCodec 上实现帧访问、路由键计算、TCP/UDP encode/decode。
//
// 生产输入为声明式 *_codec.json（每连接一份）+ 共享 errors.json。
//   - encode 管线按 c.steps 正序执行：compress（onlySmaller 先压后判）→ encrypt（guards/minBodyLen/requireKey）。
//   - bcc = xor8(body[encOffset:])（UDP 排除前 11 明文字节）—— encrypt 步 produces 的 ciphered region。
//   - 头部整体零初始化：make([]byte, headerSize) 后只写字段；未写字节恒 0；
//     checksumOut 引用的 step 未执行时写 0。
//   - 多字节字段按 endian 写；数值路由字段 math.Floor 后取整（对齐旧 oracle）。
//
// 设计说明：
//   - EncodeTCP / EncodeUDP 共用同一 encode 内部函数（codec 单 transport；方向已固化在各
//     encrypt step 的 encOffset）。两者并存用于满足当前 protocol.Adapter 接口（9 方法）。
//   - 热路径零 schema 解析、零字符串查表（编译期已全部预解析为索引/掩码/实现）。
//   - 不 import gopher-lua。
package codec

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// 只读帧访问器
// ---------------------------------------------------------------------------

// HeaderSize 返回帧头字节数（来自 schema.Frame.HeaderSize）。
//
// 该方法在 compile.go 中已定义；此处保留注释说明在 engine.go 不重复实现。
// （若 compile.go 删除该定义，这里需补回——但当前已存在，保持单一来源。）

// BodyLength 从 header 字节中读取 length 字段值，按 lengthIncludesHeader/Trailer
// 口径反算出 body 字节数（现协议 length=body 长，直接返回）。
//
// 复刻 adapter/helpers.go 的 ReadBodyLength 语义：
//   - len(header) 不足 headerSize → 返回 0；
//   - 读出的 length 字段值若 includesHeader 则减去 headerSize；
//   - 结果为负 → 0；
//   - 不做上限校验（与 helpers 行为一致；调用方按需判定）。
func (c *SchemaCodec) BodyLength(header []byte) int {
	lf := c.lengthField
	end := lf.offset + lf.size
	if len(header) < end {
		return 0
	}
	raw := readUint(header[lf.offset:lf.offset+lf.size], lf.endian, lf.kind)
	n := int64(raw)
	if c.lengthIncludesHeader {
		n -= int64(c.headerSize)
	}
	if n < 0 {
		return 0
	}
	return int(n)
}

// ExpectedRouteKey 按 routeKeySegs 拼：literal 段原样 + field 段取 route 字段值
// （route==nil 或字段缺失 → 取 0），与 decode 同源。
//
// 路由字段数值按 math.Floor 截断取整（route.cmd/act 经 JSON 反序列化为 float64）。
func (c *SchemaCodec) ExpectedRouteKey(route any) string {
	rmap := routeAsMap(route)
	// 栈上暂存 + AppendInt：本函数在发送路径每包调用，避免 Sprintf 小分配。
	var kb [64]byte
	b := kb[:0]
	for _, seg := range c.routeKeySegs {
		switch seg.segKind {
		case segKindLiteral:
			b = append(b, seg.literal...)
		case segKindField:
			// fieldIdx 指向 c.fields 中某 route 字段；其 name 即 route map key。
			if seg.fieldIdx >= 0 && seg.fieldIdx < len(c.fields) {
				fname := c.fields[seg.fieldIdx].name
				b = strconv.AppendInt(b, routeMapFloorInt(rmap, fname), 10)
			} else {
				b = append(b, '0')
			}
		}
	}
	return internRouteKey(b)
}

// ---------------------------------------------------------------------------
// encode 入口（TCP / UDP 同管线；codec 单 transport，offset 在 encrypt step.encOffset）
// ---------------------------------------------------------------------------

// EncodeTCP 编码 TCP 数据包（codec 单 transport，encrypt step.encOffset 决定明文前缀长度）。
func (c *SchemaCodec) EncodeTCP(route any, body []byte, secretKey []byte) []byte {
	return c.encode(route, body, secretKey)
}

// EncodeUDP 编码 UDP 数据包（codec 单 transport；UDP offset 已固化在 encrypt step.encOffset）。
func (c *SchemaCodec) EncodeUDP(route any, body []byte, secretKey []byte) []byte {
	return c.encode(route, body, secretKey)
}

// ---------------------------------------------------------------------------
// encode 管线核心：按 pipeline 步骤顺序处理 body，逐步累加 flags 并写头。
// ---------------------------------------------------------------------------

// encode 执行完整 encode 管线并返回 header ++ body (++ trailer)。
//
// 步骤（与 brief 第 3 节逐条对应）：
//  1. 解析 route（数值字段 math.Floor 取整）；构造 route map 供 guards 求值。
//  2. 管线正序执行 c.steps，维护 body/flags/applied/produces stash：
//     - compress：候选生效后先压缩；onlySmaller 时仅当 len(compressed)<len(body) 才采用；
//     采用 → 替换 body、置 flag、applied=true；否则丢弃、applied=false、不置 flag。
//     - encrypt：候选生效且（!requireKey || len(key)>=keyLen）才执行；前 encOffset 字节明文、
//     处理 body[encOffset:]；置 flag、applied=true；计算 produces（ciphered region 产 bcc）。
//  3. 写头：make([]byte, headerSize) 整体置零；length=wire body 长（按 includes* 调整）；
//     route 写 floor 后的整数；errorCode=0；flags=累计命名位；checksumOut=stash 产物（未执行写 0）；
//     value 按 source（const/route）；reserved=0。
//  4. 拼接返回。
func (c *SchemaCodec) encode(route any, body []byte, key []byte) []byte {
	rmap := routeAsMap(route)

	// bodyPlain 快照：管线执行前的原始 body（供 produces.bodyPlain region；现协议不用但实现要正确）。
	bodyPlain := body
	// 为了避免别名问题（compress/encrypt 可能原地改写 body 副本），保持 bodyPlain 引用稳定。
	// bodyPlain 仅在被引用时才需要——保持原引用即可。

	// 管线状态。
	work := body // 当前 body（可能被压缩/加密替换）
	flags := uint64(0)
	applied := make([]bool, len(c.steps))
	// produces stash：stepIdx -> produceName -> uint64 值（供 checksumOut 字段写入）。
	stash := make(map[int]map[string]uint64, len(c.steps))

	for i := range c.steps {
		step := &c.steps[i]
		// 候选生效判定：encodeWhen.applies + appliesWithIdx 串行依赖。
		candidate := step.encodeWhen.applies(len(work), keySatisfies(key), rmap)
		if candidate && step.encodeWhen.appliesWithIdx >= 0 {
			if step.encodeWhen.appliesWithIdx >= len(applied) || !applied[step.encodeWhen.appliesWithIdx] {
				candidate = false
			}
		}

		switch step.op {
		case opCompress:
			if !candidate {
				continue
			}
			comp, ok := step.impl.(Compressor)
			if !ok {
				continue
			}
			compressed, err := comp.Compress(work)
			if err != nil {
				// 压缩失败：不采用、applied=false、不置 flag。
				continue
			}
			// onlySmaller 特判：仅当 !onlySmaller || len(compressed)<len(work) 才采用。
			if step.encodeWhen.onlySmaller && len(compressed) >= len(work) {
				continue
			}
			work = compressed
			flags |= step.flagMask
			applied[i] = true
			// compress 通常不产 checksum；如声明了 produces（bodyFinal/bodyPlain region），
			// 在写头前再回填——现协议无此情况，这里不实现以保持自然。
			stashStep(stash, i, step, work, bodyPlain)

		case opEncrypt:
			if !candidate {
				continue
			}
			ciph, ok := step.impl.(Cipher)
			if !ok {
				continue
			}
			// requireKey/keyLen 校验：requireKey 为真时 key 须非空。
			if step.encodeWhen.requireKey && !keyLenSatisfied(step, key) {
				continue
			}
			// 产物计算（**必须在加密前**）：region "ciphered" 的语义是「本步即将处理的明文区域」，
			// 即 xor8(plaintext[encOffset:])——bcc 在加密之前对明文区域计算。
			stashStep(stash, i, step, work, bodyPlain)
			encOut, err := ciph.Encrypt(work, key, step.encOffset, step.params)
			if err != nil {
				// 加密失败：保持 applied=false（本步产物不生效）。
				// 已写入 stash 的产物需要撤回（applied=false）。
				delete(stash, i)
				continue
			}
			work = encOut
			flags |= step.flagMask
			applied[i] = true

		case opChecksum:
			// 独立 checksum 步（带 over）。v1 现协议不用，但实现正确：对 over region 计算。
			if !candidate {
				continue
			}
			chk, ok := step.impl.(Checksum)
			if !ok {
				continue
			}
			region := regionForOver(work)
			val := chk.Sum(region, nil)
			stashSingle(stash, i, step, val)
			// 独立 checksum 步通常不置 flag（无 flag 则 flagMask=0）。

		case opHash:
			// 独立 hash 步。v1 现协议不用。
			if !candidate {
				continue
			}
			h, ok := step.impl.(Hasher)
			if !ok {
				continue
			}
			region := regionForOver(work)
			hb := h.Hash(region, key, nil)
			stashSingleBytes(stash, i, step, hb)
		}
	}

	// ---- 一次性分配整包，头写入 out[:headerSize]，body/trailer 直接拷入 ----
	// out 全长零初始化：头部未写字节恒 0、trailer 恒 0，与原「header 单独 make 后 append」等价，
	// 但省去 header 的独立分配与二次拷贝（每次发送/心跳 tick 都走此路径）。
	out := make([]byte, c.headerSize+len(work)+c.trailerSize)
	header := out[:c.headerSize]
	// length：wire body 长，按 includes* 调整。
	wireBodyLen := len(work)
	lengthVal := wireBodyLen
	if c.lengthIncludesHeader {
		lengthVal += c.headerSize
	}
	if c.lengthIncludesTrailer {
		lengthVal += c.trailerSize
	}
	writeFieldUint(header, c.lengthField, uint64(lengthVal))

	// 其它字段（route/errorCode/flags/checksumOut/value/reserved）。
	for i := range c.fields {
		f := &c.fields[i]
		switch f.role {
		case roleRoute:
			writeFieldUint(header, *f, uint64(routeMapFloorInt(rmap, f.name)))
		case roleErrorCode:
			// encode 写 0（zero-init 已保证，但显式写以应对 size>1 的对齐语义）。
			writeFieldUint(header, *f, 0)
		case roleFlags:
			writeFieldUint(header, *f, flags)
		case roleChecksumOut:
			// 取 stash 中 (stepIdx, produceName) 的产物；未执行 → 0。
			val := uint64(0)
			if f.checksumRef.stepIdx >= 0 {
				if m, ok := stash[f.checksumRef.stepIdx]; ok {
					if v, ok2 := m[f.checksumRef.produceName]; ok2 {
						val = v
					}
				}
			}
			writeFieldUint(header, *f, val)
		case roleValue:
			writeFieldUint(header, *f, c.evalValueSource(f, rmap))
		case roleReserved:
			// zero-init 已保证。
		}
	}

	// ---- body 拷入头之后；trailer 区已由零初始化保证为 0 ----
	copy(out[c.headerSize:], work)
	return out
}

// ---------------------------------------------------------------------------
// encode 辅助
// ---------------------------------------------------------------------------

// keySatisfies 判定 key 是否满足 step 的 requireKey（仅看 key 是否存在/非空）。
//
// 注意：keyLen 的精确长度校验在 encrypt 分支由 keyLenSatisfied 单独判定；applies() 内的
// requireKey 仅看 key 是否存在（非空）。
func keySatisfies(key []byte) bool {
	return len(key) > 0
}

// keyLenSatisfied 判定 key 长度是否满足 step 的 keyLen 要求。
//
// 从 compiledStep.keyLen（编译期由 PipelineStep.KeyLen 填充）读取，
// 替换早期固定 `len(key)==32` 的假设。
//   - step.keyLen == 0：不校验长度（仅 requireKey 看存在性）。
//   - step.keyLen > 0：要求 len(key) >= step.keyLen（用 >= 兼容变长 key 算法，
//     如 xor_carry_rol schema 声明 keyLen=32）。
//
// 非默认 schema（如 aes_ecb keyLen=16）可正确触发加密分支。
func keyLenSatisfied(step *compiledStep, key []byte) bool {
	if step.keyLen <= 0 {
		return true // schema 未声明 keyLen，不校验
	}
	return len(key) >= step.keyLen
}

// routeAsMap 把 route any 规约为 map[string]any。
// 支持 nil、map[string]any；其它形态（struct 等）→ 空 map（路由字段取 0）。
func routeAsMap(route any) map[string]any {
	if route == nil {
		return nil
	}
	if m, ok := route.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// routeMapValue 从 route map 中取字段值（原 any 形态）。
func routeMapValue(rmap map[string]any, name string) any {
	if rmap == nil {
		return nil
	}
	return rmap[name]
}

// routeMapFloorInt 从 route map 中取字段值并 math.Floor 取整。
func routeMapFloorInt(rmap map[string]any, name string) int64 {
	v := routeMapValue(rmap, name)
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return int64(math.Floor(x))
	case float32:
		return int64(math.Floor(float64(x)))
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case uint:
		return int64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	default:
		return 0
	}
}

// evalValueSource 求 role:value 字段的取值（v1：const | route）。
func (c *SchemaCodec) evalValueSource(f *compiledField, rmap map[string]any) uint64 {
	switch f.source.kind {
	case "route":
		return uint64(routeMapFloorInt(rmap, f.source.key))
	case "const":
		return uint64(f.source.value)
	default:
		// v1 不支持的 kind 已在 Validate 拒绝；保险返回 const 值。
		return uint64(f.source.value)
	}
}

// stashStep 把 step 的所有 produces 计算并写入 stash。
//
// 各 region 的取值口径：
//   - ciphered：本步**即将处理**的明文区域 = work[step.encOffset:]（**加密前**的 work）。
//     调用方须在调用 Encrypt 之前调用本函数；bcc = xor8(plaintext[encOffset:]) 在加密前计算。
//   - bodyPlain：管线执行**前**的原始 body 快照（bodyPlain 入参）。
//   - bodyFinal：管线**当前**的 work（即该步执行后的 body；checksum/hash 步的 over 用）。
//   - header/frame：写头后才可取——v1 现协议不用，本函数按空区域处理（产 0）。
func stashStep(stash map[int]map[string]uint64, stepIdx int, step *compiledStep, work, bodyPlain []byte) {
	if len(step.produces) == 0 {
		return
	}
	m, ok := stash[stepIdx]
	if !ok {
		m = make(map[string]uint64, len(step.produces))
		stash[stepIdx] = m
	}
	for p := range step.produces {
		prod := &step.produces[p]
		var region []byte
		switch prod.region {
		case regionCiphered:
			// work 是该步执行后的 body；ciphered region = work[step.encOffset:]。
			off := max(step.encOffset, 0)
			if off > len(work) {
				off = len(work)
			}
			region = work[off:]
		case regionBodyPlain:
			region = bodyPlain
		case regionBodyFinal:
			region = work
		case regionHeader, regionFrame:
			// v1 不用；按空处理（产 0）。
			region = nil
		default:
			region = nil
		}
		var val uint64
		if prod.checksumImpl != nil {
			val = prod.checksumImpl.Sum(region, nil)
		} else if prod.hasherImpl != nil {
			hb := prod.hasherImpl.Hash(region, nil, nil)
			val = bytesToUint64(hb)
		}
		m[prod.name] = val
	}
}

// stashSingle 用于独立 checksum 步（无 produces 显式声明时也可暂存，但 v1 现协议不用）。
func stashSingle(stash map[int]map[string]uint64, stepIdx int, step *compiledStep, val uint64) {
	m, ok := stash[stepIdx]
	if !ok {
		m = make(map[string]uint64, 1)
		stash[stepIdx] = m
	}
	if len(step.produces) > 0 {
		m[step.produces[0].name] = val
	}
}

// stashSingleBytes 用于独立 hash 步。
func stashSingleBytes(stash map[int]map[string]uint64, stepIdx int, step *compiledStep, hb []byte) {
	stashSingle(stash, stepIdx, step, bytesToUint64(hb))
}

// regionForOver 计算独立 checksum/hash 步的 over region。
// v1 现协议不用；正确实现以备未来扩展。
func regionForOver(work []byte) []byte {
	// 现协议 schema 的独立 checksum 步无 Over 字段（nil），返回 work 作为安全默认。
	// Over 的真正解析需要编译期存 step.over；当前 schema 未用到。这里保守返回 work。
	return work
}

// bytesToUint64 把字节序列截断/对齐为 uint64（小端）。
// 仅用于 hash 产物写入 checksumOut 字段（v1 现协议不用 hash 产 checksumOut）。
func bytesToUint64(b []byte) uint64 {
	if len(b) == 0 {
		return 0
	}
	var buf [8]byte
	n := min(len(b), 8)
	copy(buf[:n], b[:n])
	return binary.LittleEndian.Uint64(buf[:])
}

// ---------------------------------------------------------------------------
// 字段读写 helper（按 schema 的 field 位宽/偏移读写小端整数）。
// ---------------------------------------------------------------------------

// writeFieldUint 把 v 按 field 的 endian/kind/size 写入 buf 的 [offset:offset+size]。
// 调用方需保证 buf 足够长（零初始化的 header 缓冲满足）。
func writeFieldUint(buf []byte, f compiledField, v uint64) {
	end := f.offset + f.size
	if end > len(buf) {
		return // 防御性：理论上不会触发（编译期 offset+size<=headerSize）。
	}
	switch f.kind {
	case kindBytes:
		// bytes 字段：v 解释为字面字节填充（v1 schema 中无 bytes 角色写入路径；保留）。
		for i := range f.size {
			buf[f.offset+i] = byte(v >> (uint(i) * 8))
		}
	default:
		writeUint(buf[f.offset:end], f.endian, f.kind, v)
	}
}

// writeUint 把 v 按 endian/kind 写入 dst（len(dst)==size）。
func writeUint(dst []byte, order binary.ByteOrder, kind fieldKind, v uint64) {
	switch kind {
	case kindU8, kindI8:
		dst[0] = byte(v)
	case kindU16, kindI16:
		order.PutUint16(dst, uint16(v))
	case kindU24, kindI24:
		// 24-bit：按 endian 写低 3 字节。
		var tmp [4]byte
		order.PutUint32(tmp[:], uint32(v))
		copy(dst, tmp[:3])
	case kindU32, kindI32, kindF32:
		order.PutUint32(dst, uint32(v))
	case kindU64, kindI64, kindF64:
		order.PutUint64(dst, v)
	default:
		// bytes/unknown：低字节填充。
		for i := range dst {
			dst[i] = byte(v >> (uint(i) * 8))
		}
	}
}

// readUint 按 endian/kind 从 src 读无符号整数（len(src)==size）。
func readUint(src []byte, order binary.ByteOrder, kind fieldKind) uint64 {
	switch kind {
	case kindU8, kindI8:
		return uint64(src[0])
	case kindU16, kindI16:
		return uint64(order.Uint16(src))
	case kindU24, kindI24:
		var tmp [4]byte
		copy(tmp[:3], src[:3])
		return uint64(order.Uint32(tmp[:]))
	case kindU32, kindI32:
		return uint64(order.Uint32(src))
	case kindU64, kindI64:
		return order.Uint64(src)
	default:
		var v uint64
		for i := range src {
			v |= uint64(src[i]) << (uint(i) * 8)
		}
		return v
	}
}

// ---------------------------------------------------------------------------
// decode 入口（TCP / UDP 同管线；codec 单 transport，offset 在 encrypt step.decOffset）
// ---------------------------------------------------------------------------

// DecodeTCP 解码 TCP 数据包（codec 单 transport；encrypt step.decOffset 决定明文前缀长度）。
//
// 返回 3-tuple (routeKey, body, headerErr)，签名与 protocol.Adapter.DecodeTCP 零差异：
//   - len(data) < headerSize+trailerSize → ("", nil, 0)；
//   - 管线反序执行（decrypt → decompress），每步生效与否**只看其 flag 是否在解码 flags 中置位**
//     （不重算 when/guards/minBodyLen/onlySmaller——契约 A）；
//   - encrypt 步：另要求 `!requireKey || len(key)>=step.keyLen`，否则走 onError；
//     解密成功后对 produces 的 checksumOut 产物做 bcc 校验（重算 produces 算法对 region，
//     与头里 checksumOut 字段值比对；不一致 → onError）；
//   - compress 步：Decompress 失败 → onError；
//   - onError=fail（默认）→ 返回 ("", nil, headerErr)（**body 不外泄**）；
//   - onError=keep → 保留当前 body 继续后续步骤；
//   - 最终按 routeKeySegs 拼 routeKey 返回。
func (c *SchemaCodec) DecodeTCP(data, secretKey []byte) (routeKey string, body []byte, headerErr uint64) {
	routeKey, body, headerErr, _ = c.decode(data, secretKey)
	return
}

// DecodeUDP 解码 UDP 数据包（codec 单 transport；UDP decode 偏移恒 0，固化在 encrypt step.decOffset）。
func (c *SchemaCodec) DecodeUDP(data, secretKey []byte) (routeKey string, body []byte, headerErr uint64) {
	routeKey, body, headerErr, _ = c.decode(data, secretKey)
	return
}

// DecodeTCPWithReason 与 DecodeTCP 等价，但额外返回失败原因（reason）。
// reason 为空表示解码成功；非空表示某解码步骤按 onError=fail 中止（body 未外泄），
// 供上层（SchemaAdapter）打印可追踪日志，区分"帧不完整"与"解密/解压/校验失败"。
func (c *SchemaCodec) DecodeTCPWithReason(data, secretKey []byte) (routeKey string, body []byte, headerErr uint64, reason string) {
	return c.decode(data, secretKey)
}

// DecodeUDPWithReason 与 DecodeUDP 等价，额外返回失败原因（reason），语义同 DecodeTCPWithReason。
func (c *SchemaCodec) DecodeUDPWithReason(data, secretKey []byte) (routeKey string, body []byte, headerErr uint64, reason string) {
	return c.decode(data, secretKey)
}

// decode 执行完整 decode 管线：按 pipeline 步骤逆序处理，解密/解压还原 body。
//
// 步骤（与 T1.5 brief「decode 算法」逐条对应）：
//  1. 长度校验；读头：errorCode→headerErr、route 字段→route map、flags→命名位累计值、checksumOut 原值；
//  2. body = data[headerSize : len-trailerSize]；
//  3. 管线反序：flag 置位则执行；encrypt 解密 + bcc 校验；compress 解压；失败按 onError；
//  4. routeKey 拼接。
func (c *SchemaCodec) decode(data, key []byte) (string, []byte, uint64, string) {
	// ---- 1. 长度校验 + 读头 ----
	if len(data) < c.headerSize+c.trailerSize {
		// 帧不完整属正常分包情形，非解码失败：reason 留空，不产生错误日志。
		return "", nil, 0, ""
	}
	header := data[:c.headerSize]

	var headerErr uint64
	flags := uint64(0)
	// 字段值按 fields 下标记入栈上定长数组（替代旧实现每消息 routeMap/checksumOut
	// 两个临时 map——收包热路径每帧两次 map 分配 + 哈希；字段数编译期已知且很小）。
	var routeArr, chkArr [maxHeaderScratch]uint64
	var chkSetArr [maxHeaderScratch]bool
	routeVals, chkVals, chkSet := routeArr[:], chkArr[:], chkSetArr[:]
	if n := len(c.fields); n > maxHeaderScratch {
		routeVals = make([]uint64, n)
		chkVals = make([]uint64, n)
		chkSet = make([]bool, n)
	}
	chkCount := 0

	for i := range c.fields {
		f := &c.fields[i]
		end := f.offset + f.size
		if end > len(header) {
			continue
		}
		raw := readUint(header[f.offset:end], f.endian, f.kind)
		switch f.role {
		case roleErrorCode:
			headerErr = raw
		case roleRoute:
			routeVals[i] = raw
		case roleFlags:
			// 现协议单 flags 字段：raw 即整 flags；多 flags 字段拼接亦兼容。
			flags |= raw
		case roleChecksumOut:
			chkVals[i] = raw
			chkSet[i] = true
			chkCount++
		}
	}

	// ---- 2. body ----
	// 压缩帧的 work 只是解压的输入（解压产物顶替它），是纯瞬态 scratch → 从池租借；
	// 非压缩帧的 work 会作为最终 body 外泄（Message.Data 长期持有）→ 必须独立分配。
	// flags 在读头阶段已知，租借决策先于复制。
	bodyEnd := len(data) - c.trailerSize
	n := bodyEnd - c.headerSize
	var workBuf *[]byte // 非 nil = work 当前仍是租借缓冲
	var work []byte
	if c.willInflate(flags) {
		workBuf = getWorkBuf(n)
		work = *workBuf
	} else {
		work = make([]byte, n)
	}
	copy(work, data[c.headerSize:bodyEnd])
	// releaseWork 仅在 work 被新缓冲**顶替**后调用（旧租借缓冲确定死亡才归还）。
	// 错误/keep 路径不归还：租借缓冲可能作为 body 外泄，直接交给 GC（无污染风险，
	// 只是放弃一次复用）。
	releaseWork := func() {
		if workBuf != nil {
			putWorkBuf(workBuf)
			workBuf = nil
		}
	}

	// ---- 3. 管线反序执行 ----
	// 反序：encode 是 gz(先压)→enc(后密)；decode 反过来 enc(decrypt)→gz(decompress)。
	for i := len(c.steps) - 1; i >= 0; i-- {
		step := &c.steps[i]
		// 是否执行：只看 flag 位是否在解码 flags 中置位（契约 A：不重算 when）。
		if step.flagMask != 0 && flags&step.flagMask == 0 {
			continue
		}

		switch step.op {
		case opEncrypt:
			ciph, ok := step.impl.(Cipher)
			if !ok {
				continue
			}
			// key 校验：!requireKey || len(key)>=step.keyLen；否则走 onError。
			if step.encodeWhen.requireKey && !keyLenSatisfied(step, key) {
				if step.onError == onErrorFail {
					return "", nil, headerErr, fmt.Sprintf("encrypt(step %d): 密钥长度不足（need>=%d, got=%d）", i, step.keyLen, len(key))
				}
				continue // keep：保留当前 work（密文），继续后续步骤
			}
			// work 是本函数私有副本：流密码走原地解密免整体复制（收包热路径）。
			// 失败语义与复制版一致（in-place 实现约束：报错前不动 data → keep 仍是密文）。
			if ipc, ok := step.impl.(CipherInPlace); ok {
				if err := ipc.DecryptInPlace(work, key, step.decOffset, step.params); err != nil {
					if step.onError == onErrorFail {
						return "", nil, headerErr, fmt.Sprintf("encrypt(step %d): 解密失败: %v", i, err)
					}
					continue // keep：保留当前 work
				}
			} else {
				decOut, err := ciph.Decrypt(work, key, step.decOffset, step.params)
				if err != nil {
					if step.onError == onErrorFail {
						return "", nil, headerErr, fmt.Sprintf("encrypt(step %d): 解密失败: %v", i, err)
					}
					continue // keep：保留当前 work
				}
				releaseWork() // 复制版解密顶替了 work，旧租借缓冲死亡
				work = decOut
			}
			// bcc 校验：若该步 produces 被 checksumOut 字段引用，重算并比对头里值。
			if c.verifyProducesAfterDecrypt(i, work, chkVals, chkSet, chkCount) {
				if step.onError == onErrorFail {
					return "", nil, headerErr, fmt.Sprintf("encrypt(step %d): bcc 校验失败", i)
				}
				// keep：保留解密后的 work，继续（不外泄由调用方决定）。
			}

		case opCompress:
			comp, ok := step.impl.(Compressor)
			if !ok {
				continue
			}
			// 解压去重（内存换 CPU）：大帧且解压产物此后不再被改写时按内容寻址共享。
			// 命中免解压；未命中解压后二见登记（详见 inflate_cache.go 防污染说明）。
			shareable := len(work) >= inflateDedupMinBytes && c.inflateShareSafe(i, flags)
			if shareable {
				if out := sharedInflateCache.get(work); out != nil {
					releaseWork() // 共享产物顶替 work
					work = out
					continue
				}
			}
			decOut, err := comp.Decompress(work)
			if err != nil {
				if step.onError == onErrorFail {
					return "", nil, headerErr, fmt.Sprintf("compress(step %d): 解压失败: %v", i, err)
				}
				continue // keep：保留当前 work（未解压的 gzip 流）
			}
			if shareable {
				sharedInflateCache.put(work, decOut) // put 内部快照 work，不留存租借缓冲
			}
			releaseWork()
			work = decOut

		case opChecksum, opHash:
			// 独立 checksum/hash 步在 decode 一般不执行（无 flag 已跳过；v1 现协议不用）。
			continue
		}
	}

	// ---- 4. routeKey 拼接 ----
	routeKey := c.buildDecodeRouteKey(routeVals)
	return routeKey, work, headerErr, ""
}

// maxHeaderScratch decode 头部字段暂存数组的栈上容量；字段更多的罕见 schema 回退堆分配。
const maxHeaderScratch = 16

// willInflate 判断本帧 decode 是否会执行解压步（flags 已知即可判定，
// 供 decode 在复制 body 前决定 work 缓冲租借自池还是独立分配）。
func (c *SchemaCodec) willInflate(flags uint64) bool {
	for i := range c.steps {
		step := &c.steps[i]
		if step.op != opCompress {
			continue
		}
		if step.flagMask != 0 && flags&step.flagMask == 0 {
			continue
		}
		return true
	}
	return false
}

// inflateShareSafe 判断 compress 步（下标 stepIdx）的解压产物是否可跨消息共享：
// decode 反序执行，若更低下标还有会执行的 encrypt/compress 步，解压产物随后会被
// 原地改写/顶替——共享会污染其他持有者，必须禁用。现协议 compress 恒为 decode
// 的最后执行步（encode 先压后密），恒可共享；本判定保证任意 schema 下的正确性。
func (c *SchemaCodec) inflateShareSafe(stepIdx int, flags uint64) bool {
	for j := stepIdx - 1; j >= 0; j-- {
		step := &c.steps[j]
		if step.flagMask != 0 && flags&step.flagMask == 0 {
			continue
		}
		switch step.op {
		case opEncrypt, opCompress:
			return false
		}
	}
	return true
}

// ── 压缩帧 work 缓冲池 ─────────────────────────────────────────
//
// 瞬态 scratch 池化（与 walkWireLevel 的 accs 池同性质，非"内存换 CPU"缓存交易）：
// 池内常驻 ∝ 并发解码数，GC 周期自动清空。**归还纪律**：只有 work 被解压产物 /
// 共享产物 / 复制版解密产物顶替后才归还；任何可能让缓冲作为 body 外泄的路径
// （解压失败 keep、提前 return）一律不归还，缓冲降级为普通堆分配交给 GC。

var workBufPool sync.Pool

// workBufReuses 复用命中计数（/debug/inflate 观测池化是否生效）。
var workBufReuses atomic.Uint64

func getWorkBuf(n int) *[]byte {
	if v := workBufPool.Get(); v != nil {
		p := v.(*[]byte)
		if cap(*p) >= n {
			*p = (*p)[:n]
			workBufReuses.Add(1)
			return p
		}
	}
	b := make([]byte, n)
	return &b
}

func putWorkBuf(p *[]byte) {
	workBufPool.Put(p)
}

// verifyProducesAfterDecrypt 重算 encrypt 步 produces 的 checksumOut 产物并比对头里的值。
//
// 解密后对每个 produces（region: ciphered → work[step.decOffset:]）用 produces 算法
// （xor8 等）重算；若结果与头里 checksumOut 字段值不等 → 返回 true（校验失败）。
//
// 本 engine 在 `encOffset == decOffset` 时**额外**校验 bcc：bcc 在 encode 侧对明文
// `body[encOffset:]` 计算，写入头里 checksumOut 字段；decode 侧解密后对 `work[decOffset:]`
// 重算并比对，结果不等 → 按 step.onError (fail/keep) 处理。
// 偏移对称时这两个区域描述同一段字节（明文前后自洽），校验才有意义。
//
// **偏移不对称时（现协议 UDP：encOffset=11 / decOffset=0）数学上无法校验**：encode 侧的
// `body[11:]` 与 decode 侧的 `work[0:]` 不是同一段字节，重算值必然不等——此处跳过校验。
//
// 即：TCP（对称）路径下 engine 校验 bcc（可检测篡改），UDP（非对称）路径下跳过校验。
func (c *SchemaCodec) verifyProducesAfterDecrypt(stepIdx int, work []byte, chkVals []uint64, chkSet []bool, chkCount int) bool {
	if chkCount == 0 {
		return false
	}
	step := &c.steps[stepIdx]
	if len(step.produces) == 0 {
		return false
	}
	// 偏移不对称 → 跳过校验（数学上无法比对，见上方说明）。
	if step.encOffset != step.decOffset {
		return false
	}
	for fIdx := range c.fields {
		f := &c.fields[fIdx]
		if f.role != roleChecksumOut {
			continue
		}
		if f.checksumRef.stepIdx != stepIdx {
			continue
		}
		if fIdx >= len(chkSet) || !chkSet[fIdx] {
			continue
		}
		want := chkVals[fIdx]
		// 找到对应的 produce。
		var prod *compiledProduce
		for p := range step.produces {
			if step.produces[p].name == f.checksumRef.produceName {
				prod = &step.produces[p]
				break
			}
		}
		if prod == nil {
			continue
		}
		// 重算 region（ciphered → work[decOffset:]）。
		region := decodeCipheredRegion(step, work)
		var got uint64
		if prod.checksumImpl != nil {
			got = prod.checksumImpl.Sum(region, nil)
		} else if prod.hasherImpl != nil {
			hb := prod.hasherImpl.Hash(region, nil, nil)
			got = bytesToUint64(hb)
		}
		// 按字段 size 截断 got（checksumOut 字段可能 <8 字节）。
		got = truncateUintToSize(got, f.size)
		if got != want {
			return true
		}
	}
	return false
}

// decodeCipheredRegion 返回 decrypt 后 work[decOffset:]（钳位）。
func decodeCipheredRegion(step *compiledStep, work []byte) []byte {
	off := max(step.decOffset, 0)
	if off > len(work) {
		off = len(work)
	}
	return work[off:]
}

// truncateUintToSize 按 size 截取 uint64 的低字节（checksumOut 字段 size 通常 1）。
func truncateUintToSize(v uint64, size int) uint64 {
	if size <= 0 || size >= 8 {
		return v
	}
	mask := uint64(1)<<(uint(size)*8) - 1
	return v & mask
}

// buildDecodeRouteKey 按模板拼 routeKey（routeMap 字段值来自头）。
// 与 ExpectedRouteKey 同源；routeMap 数值已是 uint64（readUint），格式化为十进制。
func (c *SchemaCodec) buildDecodeRouteKey(routeVals []uint64) string {
	// 栈上暂存 + strconv.AppendInt：替代旧 map 取值 + Sprintf（每帧多次小分配）。
	// routeVals 按字段下标存 readUint 原值（uint64），转 int64 与旧
	// decodeRouteFieldInt 的 uint64 分支一致；缺失字段值为 0（与 map miss 一致）。
	var kb [64]byte
	b := kb[:0]
	for _, seg := range c.routeKeySegs {
		switch seg.segKind {
		case segKindLiteral:
			b = append(b, seg.literal...)
		case segKindField:
			if seg.fieldIdx >= 0 && seg.fieldIdx < len(c.fields) && seg.fieldIdx < len(routeVals) {
				b = strconv.AppendInt(b, int64(routeVals[seg.fieldIdx]), 10)
			} else {
				b = append(b, '0')
			}
		}
	}
	return internRouteKey(b)
}

// ---------------------------------------------------------------------------
// DescribeError（委托 errors.go 的 DescribeError + c.errorMap）
// ---------------------------------------------------------------------------

// DescribeError 返回错误码对应中文描述；未命中返回空串 ""。
// 委托 errors.go 的 DescribeError（包级函数）+ 编译期拷贝的 c.errorMap。
// 供 protocol.SchemaAdapter.DescribeError 使用。
func (c *SchemaCodec) DescribeError(code uint64) string {
	return DescribeError(c.errorMap, code)
}
