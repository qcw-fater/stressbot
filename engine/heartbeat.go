package engine

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"time"

	"stressbot/state"
)

// 声明式心跳源类型常量。
const (
	HeartbeatSourceFixed        = "fixed"        // 固定值（Value）
	HeartbeatSourceState        = "state"        // 读取 state.Store（线程安全）
	HeartbeatSourceStateCounter = "stateCounter" // state 共享计数器自增（packageIndex 语义）
	HeartbeatSourceCounter      = "counter"      // 私有计数器（当前值；递增由调用方负责）
	HeartbeatSourceTimestamp    = "timestamp"    // 当前时间（Unit: ms|s）
	HeartbeatSourceRandomInt    = "randomInt"    // [Min,Max] 随机整数（含两端）
)

// 支持的小端字段类型宽度（字节）：整数 u8..i64 + 浮点 f32/f64（IEEE754）。
var heartbeatTypeWidth = map[string]int{
	"u8": 1, "i8": 1,
	"u16": 2, "i16": 2,
	"u32": 4, "i32": 4,
	"u64": 8, "i64": 8,
	"f32": 4, "f64": 8,
}

// HeartbeatField 声明式 raw-LE 心跳包中的一个字段。
// 框架通用布局（无游戏概念）：游戏包格式由 flow.json 配置组装，不进 Go 代码。
type HeartbeatField struct {
	Type       string   `json:"type"`                 // u8/u16/u32/u64/i8/i16/i32/i64（小端整数）/ f32/f64（小端 IEEE754 浮点）
	Source     string   `json:"source"`               // fixed/state/stateCounter/counter/timestamp/randomInt（f32/f64 仅支持 fixed/state）
	Value      *int64   `json:"value,omitempty"`      // source=fixed 且整型：固定值（nil → err，不静默默认）
	FloatValue *float64 `json:"floatValue,omitempty"` // source=fixed 且 f32/f64：固定浮点值（nil → err）
	Key        string   `json:"key,omitempty"`        // source=state|stateCounter：state 键名
	Min        *int64   `json:"min,omitempty"`        // source=randomInt：下界（含）；缺失 → err
	Max        *int64   `json:"max,omitempty"`        // source=randomInt：上界（含）；缺失 → err
	Start      *int64   `json:"start,omitempty"`      // source=counter：私有计数器初值（缺省 0）
	Step       *int64   `json:"step,omitempty"`       // source=counter：递增步长（缺省 1，由调用方应用）
	Unit       string   `json:"unit,omitempty"`       // source=timestamp："ms"|"s"，缺省 ms
}

// isFloatType 判断心跳字段类型是否为浮点（f32/f64）。浮点字段走 resolveFloatField，
// 仅支持 fixed/state 两个 source（计数器/时间戳/随机等整型语义对浮点无意义）。
func isFloatType(typ string) bool { return typ == "f32" || typ == "f64" }

// HeartbeatConfig 声明连接级心跳运行时配置（双模式 body 构造）。
// 由 codec.heartbeat 转换后交给 robot 层安装到对应 network.Connection。
//
// body 构造模式（互斥，由 codec schema 校验）：
//   - proto 模式：C2SProto != "" → 每 tick 用 factory + Bindings 构造 proto body（多数 protobuf 服心跳）；
//   - raw-binary 模式：len(Fields) > 0 → 每 tick 用 BuildHeartbeatBody 小端打包（自研协议服/战斗同步）；
//   - 空 body：两者皆无 → 静态心跳（轻量保活）。
type HeartbeatConfig struct {
	Transport  string // "tcp"|"udp"（由连接类型决定）
	Service    string // 目标服务名
	IntervalMs int    // 发送间隔（毫秒）
	Route      any    // 不透明路由（{cmd,act}），与 ActionDef.Route 同构
	// 双模式 body 构造（互斥）：
	C2SProto        string           // proto 模式：proto 全名（与 ActionDef.C2SProto 同构）
	Bindings        []FieldBind      // proto 模式：字段绑定（复用 tcpSend bindFields 解析）
	Fields          []HeartbeatField // raw-binary 模式：LE 布局；与 C2SProto 互斥
	SkipWhenMissing bool             // raw 模式 state 源缺失时跳过本 tick（true）而非报错
}

// appendLE 按 type 将 int64 值以小端字节序追加到 buf。
// 支持的类型与宽度见 heartbeatTypeWidth（整数 u8..i64 + 浮点 f32/f64）。
//
// 整数：有符号按无符号位模式写入（uintN(int64) 截断），等价 Lua 回绕掩码
//
//	u16 → &0xFFFF，u32 → &0xFFFFFFFF，u64/i64 → 全 64 位。
//
// 浮点（f32/f64）：调用方 resolveHeartbeatField 已把 IEEE754 位模式塞进 int64，
// 这里按宽度直接 PutUint 还原为小端字节，故无需为浮点单开分支。
// 未知 type 返回中文 error。
func appendLE(buf []byte, typ string, v int64) ([]byte, error) {
	width, ok := heartbeatTypeWidth[typ]
	if !ok {
		return nil, fmt.Errorf("心跳字段类型未知 type=%q", typ)
	}
	uv := uint64(v)
	switch width {
	case 1:
		buf = append(buf, byte(uv))
	case 2:
		var tmp [2]byte
		binary.LittleEndian.PutUint16(tmp[:], uint16(uv))
		buf = append(buf, tmp[:]...)
	case 4:
		var tmp [4]byte
		binary.LittleEndian.PutUint32(tmp[:], uint32(uv))
		buf = append(buf, tmp[:]...)
	case 8:
		var tmp [8]byte
		binary.LittleEndian.PutUint64(tmp[:], uv)
		buf = append(buf, tmp[:]...)
	default:
		return nil, fmt.Errorf("心跳字段类型宽度非法 type=%q width=%d", typ, width)
	}
	return buf, nil
}

// BuildHeartbeatBody 按声明式布局构造心跳 body（Go-only，零 Lua）。
//
// 参数：
//   - fields：字段布局（按顺序拼接）；
//   - st：线程安全 state.Store（读 state/stateCounter 源；stateCounter 会自增）；
//   - privateCounters：调用方持有的「私有计数器」当前值映射（key = 字段在 Fields 中的下标），
//     仅读取当前值参与打包，递增时机由调用方在「构建成功后」执行（见 netSenderAdapter）；
//   - skipWhenMissing：state 源缺失时是否跳过本 tick（true=跳过返回 skip=true，false=err）。
//
// 返回：
//   - body：组装好的小端字节流（空 fields → 空 body）；
//   - skip：本 tick 因 state 缺失被跳过（非错误）；
//   - err：配置/类型非法（不静默兜底）。
//
// 框架/业务分离：本函数只做通用 raw-LE 打包，不含任何游戏字段语义。
// 时间用 time 包，随机用 math/rand（心跳的 rtt 等模拟值无需密码学随机）。
func BuildHeartbeatBody(fields []HeartbeatField, st *state.Store, privateCounters map[int]int64, skipWhenMissing bool) (body []byte, skip bool, err error) {
	for idx := range fields {
		f := &fields[idx]
		v, fieldSkip, ferr := resolveHeartbeatField(f, idx, st, privateCounters, skipWhenMissing)
		if ferr != nil {
			return nil, false, ferr
		}
		if fieldSkip {
			return nil, true, nil
		}
		body, err = appendLE(body, f.Type, v)
		if err != nil {
			return nil, false, err
		}
	}
	return body, false, nil
}

// resolveHeartbeatField 解析单个心跳字段的 int64 值（整数=真值/位掩码；浮点=IEEE754 位模式）。
// 返回 (value, skip, err)：
//   - skip=true 表示因 skipWhenMissing 命中 state 缺失（非错误）；
//   - err 为配置/类型错误（不静默兜底）。
//
// 浮点字段（f32/f64）先经 resolveFloatField 产出 float64，再转 IEEE754 位模式塞进 int64——
// appendLE 按宽度 PutUint 即可还原小端字节，无需为浮点单开分支。
func resolveHeartbeatField(f *HeartbeatField, idx int, st *state.Store, privateCounters map[int]int64, skipWhenMissing bool) (int64, bool, error) {
	if isFloatType(f.Type) {
		fv, skip, err := resolveFloatField(f, idx, st, skipWhenMissing)
		if err != nil || skip {
			return 0, skip, err
		}
		if f.Type == "f32" {
			return int64(math.Float32bits(float32(fv))), false, nil
		}
		return int64(math.Float64bits(fv)), false, nil
	}

	switch f.Source {
	case HeartbeatSourceFixed:
		if f.Value == nil {
			return 0, false, fmt.Errorf("心跳 fixed 源缺 value 字段（idx=%d type=%q）", idx, f.Type)
		}
		return *f.Value, false, nil

	case HeartbeatSourceState:
		if f.Key == "" {
			return 0, false, fmt.Errorf("心跳 state 源缺 key 字段（idx=%d type=%q）", idx, f.Type)
		}
		got := st.Get(f.Key)
		if got == nil {
			if skipWhenMissing {
				return 0, true, nil
			}
			return 0, false, fmt.Errorf("心跳 state 源缺失 key=%q（idx=%d type=%q）", f.Key, idx, f.Type)
		}
		return state.ToInt64(got), false, nil

	case HeartbeatSourceStateCounter:
		// 共享计数器自增：state.Increment 返回递增后的新值。
		// 这是心跳打包器唯一的 state 写副作用，合法因为心跳语义本就推进包序号。
		if f.Key == "" {
			return 0, false, fmt.Errorf("心跳 stateCounter 源缺 key 字段（idx=%d type=%q）", idx, f.Type)
		}
		return st.IncrementInt64(f.Key), false, nil

	case HeartbeatSourceCounter:
		// 私有计数器：读取当前值参与打包，递增由调用方在构建成功后执行。
		// 缺失（未初始化）按 Start（缺省 0）取初值。
		v, ok := privateCounters[idx]
		if !ok {
			if f.Start != nil {
				v = *f.Start
			}
			privateCounters[idx] = v
		}
		return v, false, nil

	case HeartbeatSourceTimestamp:
		if f.Unit == "s" {
			return time.Now().Unix(), false, nil
		}
		// 缺省及任何非 "s" 值按 ms 处理（"ms" 与空都走 ms）
		return time.Now().UnixMilli(), false, nil

	case HeartbeatSourceRandomInt:
		if f.Min == nil || f.Max == nil {
			return 0, false, fmt.Errorf("心跳 randomInt 源缺 min/max 字段（idx=%d type=%q min=%v max=%v）",
				idx, f.Type, f.Min, f.Max)
		}
		lo, hi := *f.Min, *f.Max
		if lo > hi {
			return 0, false, fmt.Errorf("心跳 randomInt 源 min>max（idx=%d type=%q min=%d max=%d）",
				idx, f.Type, lo, hi)
		}
		return lo + rand.Int63n(hi-lo+1), false, nil

	default:
		return 0, false, fmt.Errorf("心跳字段未知 source=%q（idx=%d type=%q）", f.Source, idx, f.Type)
	}
}

// resolveFloatField 解析浮点字段（f32/f64）的 float64 值。
// 仅支持 fixed（FloatValue）/ state（线程安全读）；其余 source（计数器/时间戳/随机等整型语义）
// 对浮点无意义 → err（不静默兜底）。
// 返回 (value, skip, err)：skip=true 因 skipWhenMissing 命中 state 缺失（非错误）。
func resolveFloatField(f *HeartbeatField, idx int, st *state.Store, skipWhenMissing bool) (float64, bool, error) {
	switch f.Source {
	case HeartbeatSourceFixed:
		if f.FloatValue == nil {
			return 0, false, fmt.Errorf("心跳 fixed 浮点源缺 floatValue 字段（idx=%d type=%q）", idx, f.Type)
		}
		return *f.FloatValue, false, nil

	case HeartbeatSourceState:
		if f.Key == "" {
			return 0, false, fmt.Errorf("心跳 state 浮点源缺 key 字段（idx=%d type=%q）", idx, f.Type)
		}
		got := st.Get(f.Key)
		if got == nil {
			if skipWhenMissing {
				return 0, true, nil
			}
			return 0, false, fmt.Errorf("心跳 state 浮点源缺失 key=%q（idx=%d type=%q）", f.Key, idx, f.Type)
		}
		fv, ok := state.ToFloat64Safe(got)
		if !ok {
			return 0, false, fmt.Errorf("心跳 state 浮点源值非数值 key=%q（idx=%d type=%q）", f.Key, idx, f.Type)
		}
		return fv, false, nil

	default:
		return 0, false, fmt.Errorf("心跳浮点字段 type=%q 不支持 source=%q（仅 fixed/state）（idx=%d）",
			f.Type, f.Source, idx)
	}
}
