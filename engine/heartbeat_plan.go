package engine

import (
	"encoding/binary"
	"fmt"

	"stressbot/state"
)

// ---------------------------------------------------------------------------
// 心跳布局编译（注册时一次，每 tick 只覆写动态槽位）
//
// raw-binary 心跳每 tick 打出的是同一份定长小端字节流：字段个数、每个字段的
// 宽度与偏移、哪些字段恒定，注册那一刻就全部确定了。BuildHeartbeatBody 的
// 逐 tick 形态却把这些常量工作重做一遍——每字段查一次 heartbeatTypeWidth
// map、body 从 nil 起靠 append 增长（每 tick 一次分配 + 若干次扩容拷贝）、
// fixed 源明明恒定也重算重写。心跳是本进程最高频的 Go 侧循环（每连接
// 40~150ms 一发，8000 人规模下每秒数十万 tick），这些常量工作值得挪到注册时。
//
// HeartbeatPlan 是布局的编译产物：总长与「偏移/宽度已定的动态槽位表」定型，
// fixed 源在编译期直接预填进复用缓冲。每 tick 只做动态槽位的求值 + 定长写入：
//   - 零 map 查找（宽度编译期定型）；
//   - 零分配（body 缓冲跨 tick 复用；encode 把 body 拷进新分配的整包，见
//     codec/engine.go 的 out := make(...) + copy(out[headerSize:], work)，
//     且各 cipher 的 Encrypt 都是 make+copy，不原地改写入参）；
//   - fixed 槽位零重算。
//
// 不变量（对拍 BuildHeartbeatBody，见 heartbeat_plan_test.go）：
//   - 动态槽位按原字段顺序求值，state 读与 stateCounter 自增的交错语义与逐 tick
//     形态逐字一致（同一 key 既读又自增的配置，结果不变）；
//   - 编译失败（未知 type / 缺必填参数 / 浮点用了不支持的 source）不静默兜底，
//     返回中文 error，由调用方回落 BuildHeartbeatBody，保持原「每 tick 报错」
//     的可见性——坏配置不能因为多了一层编译就悄悄消失；
//   - Plan 非并发安全（持有复用缓冲 + 每 tick 覆写）：每个连接编译独立实例，
//     仅由该连接的 pump goroutine 串行调用，与 privateCounters 的现有约束同源。
// ---------------------------------------------------------------------------

// hbSlot 编译后的一个动态槽位：body 内的固定偏移 + 定长宽度 + 取值来源。
type hbSlot struct {
	off   int             // 在 body 中的字节偏移（编译期定型）
	width int             // 1/2/4/8，编译期由 heartbeatTypeWidth 定型
	idx   int             // 原 Fields 下标：错误信息定位 + privateCounters 的键
	f     *HeartbeatField // 指向 plan 内私有拷贝（Source/Key/Unit/Min/Max 只读）
}

// HeartbeatPlan raw-LE 心跳布局的编译产物（非并发安全，见文件头注释）。
type HeartbeatPlan struct {
	fields []HeartbeatField // 私有拷贝，防御外部改写 cfg 切片
	body   []byte           // 复用缓冲：fixed 槽编译期预填，动态槽每 tick 覆写
	slots  []hbSlot         // 动态槽位（按原字段顺序）
}

// CompileHeartbeatPlan 在心跳注册时把声明式 Fields 编译成定长布局。
//
// 校验与 resolveHeartbeatField / resolveFloatField 的拒绝条件一一对应（未知 type、
// fixed 缺 value/floatValue、state|stateCounter 缺 key、randomInt 缺 min/max 或
// min>max、未知 source、浮点用了 fixed/state 之外的 source），因此编译通过的布局
// 在运行期不会再因「配置非法」失败——只可能因 state 缺失而 skip。
//
// 返回 error 时调用方应回落 BuildHeartbeatBody（保持每 tick 报错），不要静默丢弃心跳。
func CompileHeartbeatPlan(fields []HeartbeatField) (*HeartbeatPlan, error) {
	p := &HeartbeatPlan{
		fields: append([]HeartbeatField(nil), fields...),
	}
	// 先算总长再分配，避免 append 增长；同时校验每个字段的类型宽度。
	size := 0
	for i := range p.fields {
		w, ok := heartbeatTypeWidth[p.fields[i].Type]
		if !ok {
			return nil, fmt.Errorf("心跳字段类型未知 type=%q（idx=%d）", p.fields[i].Type, i)
		}
		size += w
	}
	if size > 0 {
		p.body = make([]byte, size)
	}

	off := 0
	for i := range p.fields {
		f := &p.fields[i]
		width := heartbeatTypeWidth[f.Type]
		if err := validateHeartbeatField(f, i); err != nil {
			return nil, err
		}
		if f.Source == HeartbeatSourceFixed {
			// 恒定槽位：编译期算一次直接预填，运行期不再触碰。
			v, _, err := resolveHeartbeatField(f, i, nil, nil, false)
			if err != nil {
				return nil, err
			}
			putLE(p.body[off:], width, uint64(v))
		} else {
			p.slots = append(p.slots, hbSlot{off: off, width: width, idx: i, f: f})
		}
		off += width
	}
	return p, nil
}

// Size 返回编译后的 body 字节数（注册日志 / 测试用）。
func (p *HeartbeatPlan) Size() int {
	if p == nil {
		return 0
	}
	return len(p.body)
}

// Build 按编译布局产出本 tick 的 body。
//
// 语义与 BuildHeartbeatBody 完全一致（含 skip / err / 私有计数器只读不推进），
// 差别只在于：fixed 槽位不重算、无 map 查找、body 缓冲跨 tick 复用。
//
// 返回的切片是 plan 的内部复用缓冲：调用方必须在本 tick 内消费完（encode 会把它
// 拷进新分配的整包），**不得留存或跨 tick 引用**。
//
// skip 时缓冲可能只被部分覆写，但下一 tick 每个动态槽位都会被完整重写，
// 残留字节不会外泄（skip 的 tick 不发包）。
func (p *HeartbeatPlan) Build(st *state.Store, privateCounters map[int]int64, skipWhenMissing bool) (body []byte, skip bool, err error) {
	if p == nil {
		return nil, false, fmt.Errorf("心跳布局未编译（plan 为 nil）")
	}
	for i := range p.slots {
		s := &p.slots[i]
		v, fieldSkip, ferr := resolveHeartbeatField(s.f, s.idx, st, privateCounters, skipWhenMissing)
		if ferr != nil {
			return nil, false, ferr
		}
		if fieldSkip {
			return nil, true, nil
		}
		putLE(p.body[s.off:], s.width, uint64(v))
	}
	return p.body, false, nil
}

// validateHeartbeatField 编译期校验单字段配置的必填项。
// 拒绝条件与 resolveHeartbeatField / resolveFloatField 的运行期错误一一对应，
// 保证「编译通过 ⇒ 运行期不会因配置非法失败」。
func validateHeartbeatField(f *HeartbeatField, idx int) error {
	if isFloatType(f.Type) {
		switch f.Source {
		case HeartbeatSourceFixed:
			if f.FloatValue == nil {
				return fmt.Errorf("心跳 fixed 浮点源缺 floatValue 字段（idx=%d type=%q）", idx, f.Type)
			}
		case HeartbeatSourceState:
			if f.Key == "" {
				return fmt.Errorf("心跳 state 浮点源缺 key 字段（idx=%d type=%q）", idx, f.Type)
			}
		default:
			return fmt.Errorf("心跳浮点字段 type=%q 不支持 source=%q（仅 fixed/state）（idx=%d）",
				f.Type, f.Source, idx)
		}
		return nil
	}

	switch f.Source {
	case HeartbeatSourceFixed:
		if f.Value == nil {
			return fmt.Errorf("心跳 fixed 源缺 value 字段（idx=%d type=%q）", idx, f.Type)
		}
	case HeartbeatSourceState:
		if f.Key == "" {
			return fmt.Errorf("心跳 state 源缺 key 字段（idx=%d type=%q）", idx, f.Type)
		}
	case HeartbeatSourceStateCounter:
		if f.Key == "" {
			return fmt.Errorf("心跳 stateCounter 源缺 key 字段（idx=%d type=%q）", idx, f.Type)
		}
	case HeartbeatSourceCounter, HeartbeatSourceTimestamp:
		// 无必填项（counter 的 Start/Step 缺省 0/1；timestamp 的 Unit 缺省 ms）。
	case HeartbeatSourceRandomInt:
		if f.Min == nil || f.Max == nil {
			return fmt.Errorf("心跳 randomInt 源缺 min/max 字段（idx=%d type=%q min=%v max=%v）",
				idx, f.Type, f.Min, f.Max)
		}
		if *f.Min > *f.Max {
			return fmt.Errorf("心跳 randomInt 源 min>max（idx=%d type=%q min=%d max=%d）",
				idx, f.Type, *f.Min, *f.Max)
		}
	default:
		return fmt.Errorf("心跳字段未知 source=%q（idx=%d type=%q）", f.Source, idx, f.Type)
	}
	return nil
}

// putLE 按编译期定型的宽度把值小端写入 dst 的起始处（dst 长度由编译期布局保证足够）。
// 与 appendLE 的语义等价（有符号按无符号位模式截断；浮点由调用方传 IEEE754 位模式），
// 差别是定长覆写而非 append 增长。
func putLE(dst []byte, width int, uv uint64) {
	switch width {
	case 1:
		dst[0] = byte(uv)
	case 2:
		binary.LittleEndian.PutUint16(dst, uint16(uv))
	case 4:
		binary.LittleEndian.PutUint32(dst, uint32(uv))
	case 8:
		binary.LittleEndian.PutUint64(dst, uv)
	}
}

// HeartbeatCounterStep 私有计数器推进项（注册时编译）。
type HeartbeatCounterStep struct {
	Idx  int   // 字段在 Fields 中的下标（privateCounters 的键）
	Step int64 // 每次构建成功后的推进步长（Field.Step 缺省 1）
}

// CompileHeartbeatCounters 摘出 counter 源字段的推进表。
// counter 源在注册后不再变化，原实现每 tick 全字段扫一遍找它们；编译一次即可。
func CompileHeartbeatCounters(fields []HeartbeatField) []HeartbeatCounterStep {
	var steps []HeartbeatCounterStep
	for i := range fields {
		f := &fields[i]
		if f.Source != HeartbeatSourceCounter {
			continue
		}
		step := int64(1)
		if f.Step != nil {
			step = *f.Step
		}
		steps = append(steps, HeartbeatCounterStep{Idx: i, Step: step})
	}
	return steps
}

// AdvanceHeartbeatCounters 在 body 构建成功后推进私有计数器。
// read-then-increment 语义不变（首包用 Start，其后每次 +Step）。
func AdvanceHeartbeatCounters(steps []HeartbeatCounterStep, privateCounters map[int]int64) {
	for _, s := range steps {
		privateCounters[s.Idx] += s.Step
	}
}
