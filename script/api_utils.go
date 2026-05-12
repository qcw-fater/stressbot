package script

import (
	"encoding/binary"
	"encoding/hex"
	"hash/fnv"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// loadUtilsModule 加载 utils 命名空间模块。
// Lua 用法：
//
//	local utils = require("utils")
//	utils.random_int(n)            → [0, n-1] 随机整数（对齐旧 Robot.RandNumber）
//	utils.rand_range(min, max)     → [min, max] 随机整数（对齐旧 Robot.RandRangeNumber）
//	utils.random_bool()            → 随机布尔
//	utils.random_string(8)         → 随机字母数字串
//	utils.random_pick(table)       → 从数组随机选一个
//	utils.random_pick_n(table, 3)  → 从数组随机选 N 个
//	utils.weighted_pick(items, weights) → 加权随机
//	utils.rand_filter(items, count, excludes) → 排除后随机选 N 个
//	utils.rand_filter_one(items, excludes) → 排除后随机选 1 个
//	utils.shuffle(arr)             → 原地随机打乱数组
//	utils.pack_le(fmt, ...)        → 小端二进制打包
//	utils.unpack_le(data, fmt, ...) → 小端二进制解包
//	utils.sleep(1000)              → 毫秒休眠
//	utils.time_ms()                → 当前时间戳（毫秒）
//	utils.fnv_hash(version)        → FNV-1a 哈希
//
// 日志功能通过 log 模块使用：
//
//	local log = require("log")
//	log.info("message")
//	log.warn("message")
//	log.error("message")
func loadUtilsModule(L *lua.LState) int {
	mod := L.NewTable()

	// 随机
	L.SetField(mod, "random_int", L.NewFunction(utilsRandomInt))
	L.SetField(mod, "rand_range", L.NewFunction(utilsRandRange))
	L.SetField(mod, "random_bool", L.NewFunction(utilsRandomBool))
	L.SetField(mod, "random_string", L.NewFunction(utilsRandomString))
	L.SetField(mod, "random_pick", L.NewFunction(utilsRandomPick))
	L.SetField(mod, "random_pick_n", L.NewFunction(utilsRandomPickN))
	L.SetField(mod, "weighted_pick", L.NewFunction(utilsWeightedPick))
	L.SetField(mod, "rand_filter", L.NewFunction(utilsRandFilter))
	L.SetField(mod, "rand_filter_one", L.NewFunction(utilsRandFilterOne))
	L.SetField(mod, "shuffle", L.NewFunction(utilsShuffle))
	// 二进制
	L.SetField(mod, "pack_le", L.NewFunction(utilsPackLE))
	L.SetField(mod, "unpack_le", L.NewFunction(utilsUnpackLE))
	// 通用工具
	L.SetField(mod, "sleep", L.NewFunction(utilsSleep))
	L.SetField(mod, "time_ms", L.NewFunction(utilsTimeMs))
	L.SetField(mod, "fnv_hash", L.NewFunction(utilsFnvHash))

	L.Push(mod)
	return 1
}

// ---------------------------------------------------------------------------
// 随机
// ---------------------------------------------------------------------------

// utilsRandomInt utils.random_int(n) — 对齐旧 Robot.RandNumber[int]，返回 [0, n-1]
func utilsRandomInt(L *lua.LState) int {
	n := L.CheckInt(1)
	if n <= 1 {
		L.Push(lua.LNumber(0))
		return 1
	}
	L.Push(lua.LNumber(rand.Intn(n)))
	return 1
}

// utilsRandRange utils.rand_range(lo, hi) — 对齐旧 Robot.RandRangeNumber，返回 [lo, hi]
func utilsRandRange(L *lua.LState) int {
	lo := L.CheckInt(1)
	hi := L.CheckInt(2)
	if lo > hi {
		L.Push(lua.LNumber(lo))
		return 1
	}
	if lo == hi {
		L.Push(lua.LNumber(lo))
		return 1
	}
	L.Push(lua.LNumber(rand.Intn(hi-lo+1) + lo))
	return 1
}

// utilsRandomBool utils.random_bool() — 生成随机布尔值
func utilsRandomBool(L *lua.LState) int {
	L.Push(lua.LBool(rand.Intn(2) == 1))
	return 1
}

// utilsRandomString utils.random_string(length) — 生成随机字母数字串
func utilsRandomString(L *lua.LState) int {
	length := L.CheckInt(1)
	if length <= 0 {
		length = 8
	}

	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}

	L.Push(lua.LString(string(b)))
	return 1
}

// utilsRandomPick utils.random_pick(table) — 从数组中随机选一个元素
func utilsRandomPick(L *lua.LState) int {
	tb := L.CheckTable(1)
	length := tb.Len()
	if length == 0 {
		L.Push(lua.LNil)
		return 1
	}

	idx := rand.Intn(length) + 1
	L.Push(tb.RawGetInt(idx))
	return 1
}

// utilsRandomPickN utils.random_pick_n(table, n) — 从数组中随机选 N 个元素
func utilsRandomPickN(L *lua.LState) int {
	tb := L.CheckTable(1)
	n := L.CheckInt(2)

	length := tb.Len()
	if length == 0 || n <= 0 {
		L.Push(L.NewTable())
		return 1
	}

	// 收集所有元素
	all := make([]lua.LValue, 0, length)
	for i := 1; i <= length; i++ {
		all = append(all, tb.RawGetInt(i))
	}

	// Fisher-Yates 部分洗牌
	if n > length {
		n = length
	}
	for i := 0; i < n; i++ {
		j := rand.Intn(len(all)-i) + i
		all[i], all[j] = all[j], all[i]
	}

	// 构建结果表
	result := L.NewTable()
	for i := 0; i < n; i++ {
		result.RawSetInt(i+1, all[i])
	}

	L.Push(result)
	return 1
}

// utilsWeightedPick utils.weighted_pick(items, weights) — 带权随机选一个元素
// items: 数组；weights: 数组（长度需相同）。返回选中的元素及索引。
func utilsWeightedPick(L *lua.LState) int {
	items := L.CheckTable(1)
	weights := L.CheckTable(2)
	n := items.Len()
	if n == 0 {
		L.Push(lua.LNil)
		L.Push(lua.LNumber(0))
		return 2
	}
	total := 0
	ws := make([]int, n)
	for i := 1; i <= n; i++ {
		w := int(lua.LVAsNumber(weights.RawGetInt(i)))
		if w < 0 {
			w = 0
		}
		ws[i-1] = w
		total += w
	}
	if total <= 0 {
		idx := rand.Intn(n) + 1
		L.Push(items.RawGetInt(idx))
		L.Push(lua.LNumber(idx))
		return 2
	}
	r := rand.Intn(total)
	acc := 0
	for i := 0; i < n; i++ {
		acc += ws[i]
		if r < acc {
			L.Push(items.RawGetInt(i + 1))
			L.Push(lua.LNumber(i + 1))
			return 2
		}
	}
	L.Push(items.RawGetInt(n))
	L.Push(lua.LNumber(n))
	return 2
}

// utilsRandFilterOne utils.rand_filter_one(items, excludes) — 对齐 RandSilenceFilterOne
// 从 items 中随机选一个不在 excludes 内的元素；若都被排除则返回 nil。
func utilsRandFilterOne(L *lua.LState) int {
	items := L.CheckTable(1)
	excludes := L.OptTable(2, L.NewTable())

	n := items.Len()
	if n == 0 {
		L.Push(lua.LNil)
		return 1
	}

	excludeSet := make(map[string]bool)
	elen := excludes.Len()
	for i := 1; i <= elen; i++ {
		excludeSet[luaValKey(excludes.RawGetInt(i))] = true
	}

	candidates := make([]lua.LValue, 0, n)
	for i := 1; i <= n; i++ {
		v := items.RawGetInt(i)
		if !excludeSet[luaValKey(v)] {
			candidates = append(candidates, v)
		}
	}
	if len(candidates) == 0 {
		L.Push(lua.LNil)
		return 1
	}
	L.Push(candidates[rand.Intn(len(candidates))])
	return 1
}

// utilsRandFilter utils.rand_filter(items, excludes, count) — 从 items 中过滤掉 excludes 后随机选 count 个
func utilsRandFilter(L *lua.LState) int {
	items := L.CheckTable(1)
	excludes := L.OptTable(2, L.NewTable())
	count := L.OptInt(3, 1)

	excludeSet := make(map[string]bool)
	elen := excludes.Len()
	for i := 1; i <= elen; i++ {
		excludeSet[luaValKey(excludes.RawGetInt(i))] = true
	}

	n := items.Len()
	candidates := make([]lua.LValue, 0, n)
	for i := 1; i <= n; i++ {
		v := items.RawGetInt(i)
		if !excludeSet[luaValKey(v)] {
			candidates = append(candidates, v)
		}
	}

	if count > len(candidates) {
		count = len(candidates)
	}
	for i := 0; i < count; i++ {
		j := rand.Intn(len(candidates)-i) + i
		candidates[i], candidates[j] = candidates[j], candidates[i]
	}

	result := L.NewTable()
	for i := 0; i < count; i++ {
		result.RawSetInt(i+1, candidates[i])
	}
	L.Push(result)
	return 1
}

// utilsShuffle utils.shuffle(arr) — 原地随机打乱数组，返回自身。
func utilsShuffle(L *lua.LState) int {
	tb := L.CheckTable(1)
	n := tb.Len()
	for i := n; i > 1; i-- {
		j := rand.Intn(i) + 1
		vi := tb.RawGetInt(i)
		vj := tb.RawGetInt(j)
		tb.RawSetInt(i, vj)
		tb.RawSetInt(j, vi)
	}
	L.Push(tb)
	return 1
}

// ---------------------------------------------------------------------------
// 二进制
// ---------------------------------------------------------------------------

// utilsPackLE utils.pack_le(format, values...) — 小端二进制打包
// format 字符: u8/i8, u16/i16, u32/i32, u64/i64, f32, f64
// i64/u64 支持字符串形式的大数字（超过 2^53 的 snowflake ID）
// 返回: 二进制字符串
func utilsPackLE(L *lua.LState) int {
	format := L.CheckString(1)

	buf := make([]byte, 0, 64)

	for i := 2; i <= L.GetTop(); i++ {
		v := L.Get(i)

		switch format {
		case "u8":
			n := lua.LVAsNumber(v)
			buf = append(buf, byte(n))
		case "i8":
			n := lua.LVAsNumber(v)
			buf = append(buf, byte(int8(n)))
		case "u16":
			n := lua.LVAsNumber(v)
			b := make([]byte, 2)
			binary.LittleEndian.PutUint16(b, uint16(n))
			buf = append(buf, b...)
		case "i16":
			n := lua.LVAsNumber(v)
			b := make([]byte, 2)
			binary.LittleEndian.PutUint16(b, uint16(int16(n)))
			buf = append(buf, b...)
		case "u32":
			n := lua.LVAsNumber(v)
			b := make([]byte, 4)
			binary.LittleEndian.PutUint32(b, uint32(n))
			buf = append(buf, b...)
		case "i32":
			n := lua.LVAsNumber(v)
			b := make([]byte, 4)
			binary.LittleEndian.PutUint32(b, uint32(int32(n)))
			buf = append(buf, b...)
		case "u64":
			n := parseUint64(v)
			b := make([]byte, 8)
			binary.LittleEndian.PutUint64(b, n)
			buf = append(buf, b...)
		case "i64":
			n := parseInt64(v)
			b := make([]byte, 8)
			binary.LittleEndian.PutUint64(b, uint64(n))
			buf = append(buf, b...)
		case "f32":
			n := float32(lua.LVAsNumber(v))
			b := make([]byte, 4)
			binary.LittleEndian.PutUint32(b, math.Float32bits(n))
			buf = append(buf, b...)
		case "f64":
			n := float64(lua.LVAsNumber(v))
			b := make([]byte, 8)
			binary.LittleEndian.PutUint64(b, math.Float64bits(n))
			buf = append(buf, b...)
		default:
			L.RaiseError("unknown pack format: %s", format)
			return 0
		}
	}

	L.Push(lua.LString(string(buf)))
	return 1
}

// utilsUnpackLE utils.unpack_le(data, fmt1, fmt2, ...) — 小端二进制解包
// data: 二进制字符串（pack_le 的产物）
// fmt1..fmtN: 每个字段的格式（u8/i8/u16/i16/u32/i32/u64/i64/f32/f64）
// 返回: 按格式顺序返回各字段值；i64/u64 超过 2^53 的返回字符串
func utilsUnpackLE(L *lua.LState) int {
	data := []byte(L.CheckString(1))

	offset := 0
	for i := 2; i <= L.GetTop(); i++ {
		format := L.CheckString(i)
		switch format {
		case "u8":
			if offset+1 > len(data) {
				L.RaiseError("unpack_le: 数据不足 (u8 at offset %d)", offset)
				return 0
			}
			L.Push(lua.LNumber(data[offset]))
			offset++
		case "i8":
			if offset+1 > len(data) {
				L.RaiseError("unpack_le: 数据不足 (i8 at offset %d)", offset)
				return 0
			}
			L.Push(lua.LNumber(int8(data[offset])))
			offset++
		case "u16":
			if offset+2 > len(data) {
				L.RaiseError("unpack_le: 数据不足 (u16 at offset %d)", offset)
				return 0
			}
			L.Push(lua.LNumber(binary.LittleEndian.Uint16(data[offset:])))
			offset += 2
		case "i16":
			if offset+2 > len(data) {
				L.RaiseError("unpack_le: 数据不足 (i16 at offset %d)", offset)
				return 0
			}
			L.Push(lua.LNumber(int16(binary.LittleEndian.Uint16(data[offset:]))))
			offset += 2
		case "u32":
			if offset+4 > len(data) {
				L.RaiseError("unpack_le: 数据不足 (u32 at offset %d)", offset)
				return 0
			}
			L.Push(lua.LNumber(binary.LittleEndian.Uint32(data[offset:])))
			offset += 4
		case "i32":
			if offset+4 > len(data) {
				L.RaiseError("unpack_le: 数据不足 (i32 at offset %d)", offset)
				return 0
			}
			L.Push(lua.LNumber(int32(binary.LittleEndian.Uint32(data[offset:]))))
			offset += 4
		case "u64":
			if offset+8 > len(data) {
				L.RaiseError("unpack_le: 数据不足 (u64 at offset %d)", offset)
				return 0
			}
			v := binary.LittleEndian.Uint64(data[offset:])
			if v > uint64(1<<53) {
				L.Push(lua.LString(strconv.FormatUint(v, 10)))
			} else {
				L.Push(lua.LNumber(v))
			}
			offset += 8
		case "i64":
			if offset+8 > len(data) {
				L.RaiseError("unpack_le: 数据不足 (i64 at offset %d)", offset)
				return 0
			}
			v := int64(binary.LittleEndian.Uint64(data[offset:]))
			if v > 1<<53 || v < -(1<<53) {
				L.Push(lua.LString(strconv.FormatInt(v, 10)))
			} else {
				L.Push(lua.LNumber(v))
			}
			offset += 8
		case "f32":
			if offset+4 > len(data) {
				L.RaiseError("unpack_le: 数据不足 (f32 at offset %d)", offset)
				return 0
			}
			bits := binary.LittleEndian.Uint32(data[offset:])
			L.Push(lua.LNumber(math.Float32frombits(bits)))
			offset += 4
		case "f64":
			if offset+8 > len(data) {
				L.RaiseError("unpack_le: 数据不足 (f64 at offset %d)", offset)
				return 0
			}
			bits := binary.LittleEndian.Uint64(data[offset:])
			L.Push(lua.LNumber(math.Float64frombits(bits)))
			offset += 8
		default:
			L.RaiseError("unknown unpack format: %s", format)
			return 0
		}
	}

	return L.GetTop() - 1
}

// ---------------------------------------------------------------------------
// 通用工具
// ---------------------------------------------------------------------------

// utilsSleep utils.sleep(ms) — 休眠指定毫秒数（响应 context 取消）。
// 休眠期间释放 luaMu，避免阻塞心跳 builder 和监听回调。
func utilsSleep(L *lua.LState) int {
	ms := L.CheckInt(1)
	if ms > 0 {
		ctx := GetContext(L)
		if ctx != nil && ctx.Ctx != nil {
			withReleasedMu(ctx.LuaMu, func() {
				select {
				case <-time.After(time.Duration(ms) * time.Millisecond):
				case <-ctx.Ctx.Done():
				}
			})
		} else {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	}
	return 0
}

// utilsTimeMs utils.time_ms() — 获取当前时间戳（毫秒）
func utilsTimeMs(L *lua.LState) int {
	L.Push(lua.LNumber(time.Now().UnixMilli()))
	return 1
}

// utilsFnvHash utils.fnv_hash(version) — FNV-1a 64位哈希，返回十六进制字符串
// 与旧 Robot 的 versionHashFNV 函数行为一致
func utilsFnvHash(L *lua.LState) int {
	version := strings.TrimSpace(L.CheckString(1))
	version = strings.TrimPrefix(version, "v")
	version = strings.TrimPrefix(version, "V")
	norm := strings.ToLower(version)
	h := fnv.New64a()
	_, _ = h.Write([]byte(norm))
	L.Push(lua.LString(hex.EncodeToString(h.Sum(nil))))
	return 1
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// luaValKey 将 Lua 值转为可比较的字符串 key
func luaValKey(v lua.LValue) string {
	switch v.Type() {
	case lua.LTNumber:
		return "n:" + strconv.FormatFloat(float64(v.(lua.LNumber)), 'f', -1, 64)
	case lua.LTString:
		return "s:" + string(v.(lua.LString))
	case lua.LTBool:
		if bool(v.(lua.LBool)) {
			return "b:1"
		}
		return "b:0"
	default:
		return "x:" + v.String()
	}
}

// parseInt64 从 Lua 值解析 int64（支持字符串大数字）
func parseInt64(v lua.LValue) int64 {
	switch n := v.(type) {
	case lua.LNumber:
		return int64(n)
	case lua.LString:
		if i, err := strconv.ParseInt(string(n), 10, 64); err == nil {
			return i
		}
		return 0
	default:
		return int64(lua.LVAsNumber(v))
	}
}

// parseUint64 从 Lua 值解析 uint64（支持字符串大数字）
func parseUint64(v lua.LValue) uint64 {
	switch n := v.(type) {
	case lua.LNumber:
		return uint64(n)
	case lua.LString:
		if u, err := strconv.ParseUint(string(n), 10, 64); err == nil {
			return u
		}
		return 0
	default:
		return uint64(lua.LVAsNumber(v))
	}
}
