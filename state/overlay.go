// overlay.go 提供 Overlay——不可变导航基座（PathNavigator，如 protox.WireValue /
// protox.Frozen）之上的可变覆盖层，是 SetPath 对 wire-first 整存值做写时物化（COW）
// 的写模型：
//
//   - 读：先查 overrides（含删除墓碑），未命中回落基座惰性导航；
//   - 写：SetPath 沿写路径把「基座的子节点」按需物化进 overrides（message 子节点
//     继续包 Overlay，repeated/map 终端物化为可变 []any / map[string]any），
//     未触碰的兄弟子树保持基座字节形态（零展开）；
//   - 常驻体积 ∝ 实际写入的路径数，而非消息 schema 大小。
//
// 并发契约与嵌套容器一致：Overlay 的 overrides 只由执行器 goroutine 在 Store 写锁内
// 改写；读经 Store 读锁（GetPath）或执行器自身，无并发写。
package state

// tombstone 标记 overlay 中被置 nil 的键：读视为不存在，物化时从基座结果中删除。
// （与旧 map 语义的差别：map[seg]=nil 保键、这里除键；两者经 GetPath 读到的都是 nil。）
type tombstone struct{}

// Overlay 不可变基座 + 可变覆盖层。实现 PathNavigator 与 ValueMaterializer。
type Overlay struct {
	base      PathNavigator
	overrides map[string]any
}

// NewOverlay 用不可变基座创建空覆盖层。
func NewOverlay(base PathNavigator) *Overlay {
	return &Overlay{base: base, overrides: make(map[string]any, 4)}
}

// NavigateSegs 实现 PathNavigator：覆盖层优先，未覆盖回落基座。
func (o *Overlay) NavigateSegs(segs []string) (any, bool) {
	if o == nil || len(segs) == 0 {
		return nil, false
	}
	seg := segs[0]
	if isArrayIndex(seg) {
		// 基座是 message 语义节点（map 键空间），数组下标段不匹配。
		return nil, false
	}
	if v, hit := o.overrides[seg]; hit {
		if _, dead := v.(tombstone); dead {
			return nil, false
		}
		if len(segs) == 1 {
			return v, true
		}
		return navigateFrom(v, segs[1:])
	}
	return o.base.NavigateSegs(segs)
}

// navigateFrom 在任意值上逐段导航（GetPath 内层循环的无锁版本，供覆盖层内值使用）。
func navigateFrom(v any, segs []string) (any, bool) {
	cur := v
	for i := 0; i < len(segs); i++ {
		if cur == nil {
			return nil, false
		}
		if nav, ok := cur.(PathNavigator); ok {
			return nav.NavigateSegs(segs[i:])
		}
		cur = navigateValue(cur, segs[i])
	}
	if cur == nil {
		return nil, false
	}
	return cur, true
}

// MaterializeValue 实现 ValueMaterializer：基座全量物化后套用覆盖层，
// 产出纯 map[string]any 树（嵌套导航值递归物化；容器复制，不改写内部状态）。
func (o *Overlay) MaterializeValue() any {
	var out map[string]any
	if mz, ok := o.base.(ValueMaterializer); ok {
		if bm, ok2 := mz.MaterializeValue().(map[string]any); ok2 {
			out = bm
		}
	}
	if out == nil {
		out = make(map[string]any, len(o.overrides))
	}
	for k, v := range o.overrides {
		if _, dead := v.(tombstone); dead {
			delete(out, k)
			continue
		}
		out[k] = materializeAny(v)
	}
	return out
}

// materializeAny 递归把值物化为纯 Go 树：导航值全量展开，容器复制。
func materializeAny(v any) any {
	switch x := v.(type) {
	case ValueMaterializer:
		return materializeAny(x.MaterializeValue())
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = materializeAny(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = materializeAny(e)
		}
		return out
	default:
		return v
	}
}

// childForWrite 返回 seg 对应的可写子容器，供 SetPath 中间段下探（写锁内调用）。
// 覆盖层已有该键 → 按需转可写并回写；未覆盖 → 从基座物化该子节点进覆盖层。
// found=false 表示该段不可写（如数组下标越界语义），SetPath 按旧语义跳过整次写入。
func (o *Overlay) childForWrite(seg string) (any, bool) {
	if isArrayIndex(seg) {
		return nil, false
	}
	if v, hit := o.overrides[seg]; hit {
		if _, dead := v.(tombstone); dead {
			m := make(map[string]any)
			o.overrides[seg] = m
			return m, true
		}
		switch x := v.(type) {
		case map[string]any, []any, *Overlay:
			return x, true
		case PathNavigator:
			ov := NewOverlay(x)
			o.overrides[seg] = ov
			return ov, true
		default:
			m := make(map[string]any)
			o.overrides[seg] = m
			return m, true
		}
	}
	// 未覆盖：从基座取该子节点的惰性表示并物化进覆盖层。
	bv, ok := o.base.NavigateSegs([]string{seg})
	if !ok || bv == nil {
		m := make(map[string]any)
		o.overrides[seg] = m
		return m, true
	}
	switch x := bv.(type) {
	case PathNavigator:
		ov := NewOverlay(x)
		o.overrides[seg] = ov
		return ov, true
	case map[string]any, []any:
		// 基座导航产物是现场新建容器，覆盖层直接接管所有权。
		o.overrides[seg] = x
		return x, true
	default:
		// 标量：与旧语义一致，覆盖为新 map 继续下探。
		m := make(map[string]any)
		o.overrides[seg] = m
		return m, true
	}
}

// setChild 终端赋值（写锁内调用）。nil 写墓碑（读回 nil，物化时删键）。
func (o *Overlay) setChild(seg string, val any) {
	if isArrayIndex(seg) {
		return
	}
	if val == nil {
		o.overrides[seg] = tombstone{}
		return
	}
	o.overrides[seg] = val
}
