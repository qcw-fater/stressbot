package protox

// ── 导航路径驻留表：一次哈希查表同时服务「影子采样判定」与「预解析 fd 链」──
//
// 动机（8000 人压测 profile）：wire 读路径（NavigateSegs / GetFieldCompat /
// MaterializeAllowed）每次调用旧 shadowShouldVerify 都要
//   name + "\x00" + strings.Join(segs, ".") 拼 key，
//   再对两张 sync.Map 做 LoadOrStore（其 new(atomic.Int32/64) 实参每次都逃逸）
// ——热路径上白付 4 次堆分配；字段解析还要每层 Fields().ByName 查一遍。
//
// 本表把 (schema, 路径) 驻留为一个条目：maphash(全名+分段) → *navPathInfo，
// 稳态读路径 = 1 次哈希 + 1 次 COW map 查找 + 原子计数，零分配、零字符串拼接；
// 条目同时携带按 wireNavigate 层级推进规则预解析的 fd 链，运行时免逐层 ByName。
//
// 影子采样语义保持（相对旧 shadowShouldVerify）：
//   - 首 K 次全查按 (schema, 路径) 计数、稳态按 per-schema 周期采样——不变；
//   - 差异 1：哈希碰撞或表满（navInfoMaxEntries，防脚本动态 map-key 路径把表
//     撑爆——旧实现的 sync.Map 反而无界）时该路径不驻留：跳过首 K，仅参与
//     per-schema 稳态采样；
//   - 差异 2：条目按 desc **身份**（接口指针）校验——proto 热重载后新描述符
//     替换旧条目并重新首 K 全查（旧实现按名字计数，重载后不重查；新表更严格，
//     顺带解除对已关闭 Factory 描述符树的钉扎）。
//
// fd 链正确性：fds[i] 非 nil 时恒等于运行时第 i 段所在层级的
// Fields().ByName(segs[i])（编译与运行时走同一套层级推进规则，描述符链
// 由根描述符唯一确定）；非字段段（map key / 下标）与编译提前终止的位置留
// nil，运行时回退 ByName，行为不变。

import (
	"hash/maphash"
	"maps"
	"sync/atomic"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// navInfoMaxEntries 驻留表容量上限。配置固定的路径集合远小于此；超限的只能是
// 动态构造的路径（如以 map key 为段），不驻留也只是回到逐层 ByName 的旧成本。
const navInfoMaxEntries = 8192

// navPathInfo 一个 (schema, 路径) 的驻留条目。
type navPathInfo struct {
	desc protoreflect.MessageDescriptor // 身份校验（指针相等）；重载后被替换
	segs []string                       // 碰撞校验用快照
	fds  []protoreflect.FieldDescriptor // 预解析 fd 链（非字段段/编译终止处为 nil）

	seen      atomic.Int32  // 影子首 K 全查计数
	schemaCnt *atomic.Int64 // 所属 schema 稳态采样计数（跨路径共享）
}

var (
	navSeed      = maphash.MakeSeed()
	navInfoTable atomic.Pointer[map[uint64]*navPathInfo]
	navSchemaCnt atomic.Pointer[map[string]*atomic.Int64]
)

// navHash (schema 全名, 路径段) 的驻留键。64 位哈希碰撞概率对本表规模可忽略，
// 且命中后还有 desc 身份 + 逐段字符串校验，碰撞只导致「不驻留」，不会错用条目。
func navHash(name string, segs []string) uint64 {
	var h maphash.Hash
	h.SetSeed(navSeed)
	h.WriteString(name)
	for _, s := range segs {
		h.WriteByte(0)
		h.WriteString(s)
	}
	return h.Sum64()
}

func navSegsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// navSchemaCntFor 取（或建）schema 的稳态采样计数器。COW 写、无锁读；
// schema 集合有界（配置的 proto 全集），无需容量上限。
func navSchemaCntFor(name string) *atomic.Int64 {
	for {
		old := navSchemaCnt.Load()
		var m map[string]*atomic.Int64
		if old != nil {
			m = *old
			if c, ok := m[name]; ok {
				return c
			}
		}
		nm := make(map[string]*atomic.Int64, len(m)+1)
		maps.Copy(nm, m)
		c := new(atomic.Int64)
		nm[name] = c
		if navSchemaCnt.CompareAndSwap(old, &nm) {
			return c
		}
	}
}

// navInfoFor 查驻留条目；未驻留则编译并尝试插入。
// 返回 nil 表示不可驻留（表满 / 槽位被真哈希碰撞占用），调用方按未缓存处理。
func navInfoFor(desc protoreflect.MessageDescriptor, segs []string) *navPathInfo {
	name := string(desc.FullName())
	h := navHash(name, segs)
	if mp := navInfoTable.Load(); mp != nil {
		if e := (*mp)[h]; e != nil && e.desc == desc && navSegsEqual(e.segs, segs) {
			return e
		}
	}

	var info *navPathInfo
	for {
		old := navInfoTable.Load()
		var m map[uint64]*navPathInfo
		if old != nil {
			m = *old
		}
		if e := m[h]; e != nil {
			if e.desc == desc && navSegsEqual(e.segs, segs) {
				return e // 竞争者已插入同条目
			}
			// 槽位被占：同 (名字, 路径) 但 desc 身份不同 = proto 重载 → 替换；
			// 名字或路径不同 = 真哈希碰撞 → 放弃驻留。
			if string(e.desc.FullName()) != name || !navSegsEqual(e.segs, segs) {
				return nil
			}
		} else if len(m) >= navInfoMaxEntries {
			return nil
		}
		if info == nil {
			segsCopy := make([]string, len(segs))
			copy(segsCopy, segs)
			info = &navPathInfo{
				desc:      desc,
				segs:      segsCopy,
				fds:       compileNavFds(desc, segsCopy),
				schemaCnt: navSchemaCntFor(name),
			}
		}
		nm := make(map[uint64]*navPathInfo, len(m)+1)
		maps.Copy(nm, m)
		nm[h] = info
		if navInfoTable.CompareAndSwap(old, &nm) {
			return info
		}
	}
}

// compileNavFds 按 wireNavigate 的层级推进规则预解析各段的 FieldDescriptor。
// 推进规则必须与 wireNavigate 逐字对齐（map 消费两段、list 消费两段、单数
// message 消费一段、标量终止）；遇到运行时必然返回 false 的形态（下标开头、
// 字段不存在、标量后仍有剩余段）就地终止，剩余槽位留 nil。
func compileNavFds(md protoreflect.MessageDescriptor, segs []string) []protoreflect.FieldDescriptor {
	fds := make([]protoreflect.FieldDescriptor, len(segs))
	cur := md
	for i := 0; i < len(segs) && cur != nil; {
		if isIndexSeg(segs[i]) {
			break
		}
		fd := cur.Fields().ByName(protoreflect.Name(segs[i]))
		if fd == nil {
			break
		}
		fds[i] = fd
		switch {
		case fd.IsMap():
			// segs[i+1] 是 map key（非字段段）；value 为 message 才可能继续下钻。
			if fd.MapValue().Kind() == protoreflect.MessageKind {
				cur = fd.MapValue().Message()
			} else {
				cur = nil
			}
			i += 2
		case fd.IsList():
			// segs[i+1] 是下标段；元素为 message 才可能继续下钻。
			if fd.Kind() == protoreflect.MessageKind {
				cur = fd.Message()
			} else {
				cur = nil
			}
			i += 2
		case fd.Kind() == protoreflect.MessageKind:
			cur = fd.Message()
			i++
		default:
			cur = nil // 标量/group 终端，其后段运行时返回未找到
			i++
		}
	}
	return fds
}

// fdsTail 与 parts[n:] 同步推进 fd 链；链短于消费量（未编译）时返回 nil。
func fdsTail(fds []protoreflect.FieldDescriptor, n int) []protoreflect.FieldDescriptor {
	if len(fds) >= n {
		return fds[n:]
	}
	return nil
}

// navResolve wire 读路径的统一入口：返回 (预解析 fd 链, 本次是否影子校验)。
// 影子判定语义与旧 shadowShouldVerify 一致（差异见文件头注释）。
func navResolve(desc protoreflect.MessageDescriptor, segs []string) ([]protoreflect.FieldDescriptor, bool) {
	shadowNavigates.Add(1)
	info := navInfoFor(desc, segs)
	var fds []protoreflect.FieldDescriptor
	if info != nil {
		fds = info.fds
	}
	if !wireShadowOn.Load() {
		return fds, false
	}
	if info != nil {
		if info.seen.Load() < shadowFirstK {
			info.seen.Add(1)
			return fds, true
		}
		return fds, info.schemaCnt.Add(1)%shadowSampleEvery == 0
	}
	// 未驻留（碰撞/表满）：无首 K 计数位，仅参与 per-schema 稳态采样。
	return nil, navSchemaCntFor(string(desc.FullName())).Add(1)%shadowSampleEvery == 0
}

// navResetAll 清空驻留表与 schema 计数（Factory.Close / 测试隔离用）。
// 清表解除对旧描述符树的钉扎；下个任务的路径按需重编译、重新首 K 全查，
// 只多花一次性成本，方向安全。
func navResetAll() {
	navInfoTable.Store(nil)
	navSchemaCnt.Store(nil)
}
