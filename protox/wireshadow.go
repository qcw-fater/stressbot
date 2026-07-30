package protox

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ── Wire 扫描影子验证（L2/L3）与 schema 级降级 ─────────────────────
//
// 三层正确性防线的线上两层（L1 是离线差分 fuzz，见 wirediff_test.go）：
//   - L2 首访全查：每个 (schema, 路径) 组合的前 shadowFirstK 次导航，
//     用「dynamicpb 解码 + Frozen 导航」做 oracle 双读比对；
//   - L3 稳态采样：之后每 schema 每 shadowSampleEvery 次导航抽查一次。
//
// 失配处理（fail-safe，不 fail-loud——正确性优先于形态）：
//   - 记录失配详情（schema/路径/两侧值）到 error 日志与统计；
//   - 以解码侧（oracle）结果返回本次导航；
//   - 把该 schema 标记为降级（WireDegraded）：后续**存储点**不再为它构造 WireValue，
//     回落解码态 Frozen 路径。已存的 WireValue 继续可读（每次读都走 oracle 兜底）。
//
// 开销预算：L2 是 O(路径集合)（配置固定、有界）一次性成本；L3 采样率 1/8192，
// 单次校验 = 一次消息解码 + 两侧物化比对（微秒~百微秒级），摊薄后可忽略。
// 稳态可用 SetWireShadowEnabled(false) 全关（容量压测建议先开满一轮再关）。

const (
	// shadowFirstK 每个 (schema, 路径) 首访全查次数。
	shadowFirstK = 3
	// shadowSampleEvery 稳态采样周期（每 schema 每 N 次导航抽查 1 次）。
	shadowSampleEvery = 8192
)

var (
	wireShadowOn atomic.Bool

	// 首 K / 稳态采样计数已并入导航驻留表（wirenav.go navResolve），
	// 与预解析 fd 链共用一次查表，热路径零分配。

	// degradedSchemas schemaFullName → 首次失配原因（string）。
	degradedSchemas sync.Map

	shadowNavigates atomic.Int64
	shadowChecks    atomic.Int64
	shadowMismatch  atomic.Int64
)

func init() {
	wireShadowOn.Store(true)
}

// SetWireShadowEnabled 开/关线上影子验证（灰度期开启，稳态可关闭省 CPU）。
func SetWireShadowEnabled(on bool) { wireShadowOn.Store(on) }

// WireDegraded 判断 schema 是否已因影子失配降级（存储点据此回落解码路径）。
func WireDegraded(desc protoreflect.MessageDescriptor) bool {
	if desc == nil {
		return false
	}
	_, bad := degradedSchemas.Load(string(desc.FullName()))
	return bad
}

// MaterializeAllowed 决定这次全量物化能否走 wire 直转（WalkWire）：
//   - schema 已降级 → false（回落解码路径）；
//   - 影子采样命中（整树伪路径 "*"，首 K 次全查 + 稳态抽样）→ 现场双读比对，
//     失配记录并降级后返回 false；
//   - 其余 → true。
func (wv *WireValue) MaterializeAllowed() bool {
	if wv == nil || wv.desc == nil {
		return false
	}
	if WireDegraded(wv.desc) {
		return false
	}
	if _, verify := navResolve(wv.desc, shadowWholeTreeSegs); verify {
		return shadowVerifyMaterialize(wv)
	}
	return true
}

// shadowWholeTreeSegs 整树物化在影子计数体系里的伪路径。
var shadowWholeTreeSegs = []string{"*"}

// shadowVerifyMaterialize 直转整树 vs dynamicpb 解码整树的双读比对。
// 失配（或直转失败）记录并降级 schema，返回 false 让调用方回落解码路径。
func shadowVerifyMaterialize(wv *WireValue) bool {
	shadowChecks.Add(1)
	sink := newMapTreeSink()
	if err := wv.Walk(sink); err != nil {
		recordWireMismatch(wv, shadowWholeTreeSegs, "直转失败: "+err.Error())
		return false
	}
	msg, err := wv.Message()
	if err != nil {
		recordWireMismatch(wv, shadowWholeTreeSegs, "oracle 解码失败: "+err.Error())
		return false
	}
	oracle := messageToMap(msg.ProtoReflect())
	if !plainEqual(sink.m, oracle) {
		recordWireMismatch(wv, shadowWholeTreeSegs, fmt.Sprintf("wire=%v oracle=%v",
			summarizeValue(sink.m), summarizeValue(oracle)))
		return false
	}
	return true
}

// shadowVerifyNavigate 执行一次双读比对；失配时以 oracle 结果返回并降级该 schema。
func shadowVerifyNavigate(wv *WireValue, segs []string, got any, found bool) (any, bool) {
	shadowChecks.Add(1)
	msg, err := wv.Message()
	if err != nil {
		// 构造点已结构校验，解码失败意味着校验器与解码器判定不一致——按失配降级。
		recordWireMismatch(wv, segs, "oracle 解码失败: "+err.Error())
		return nil, false
	}
	oracleVal, oracleFound := Freeze(msg).NavigateSegs(segs)
	if found != oracleFound || !plainEqual(materializePlain(got), materializePlain(oracleVal)) {
		recordWireMismatch(wv, segs, fmt.Sprintf("wire=(%v,%t) oracle=(%v,%t)",
			summarizeValue(got), found, summarizeValue(oracleVal), oracleFound))
		return oracleVal, oracleFound
	}
	return got, found
}

// mismatchDumpCap 失配日志里 wire 字节 hex 转储的上限（超出部分截断，rawLen 给全长）。
const mismatchDumpCap = 4096

// recordWireMismatch 记录失配并把 schema 降级。
// 日志必须携带离线复现所需的全部要素：schema 全名、访问路径、两侧产物摘要、
// 以及原始 wire 字节的 hex 转储（截断到 mismatchDumpCap）——拿到日志即可写出
// 复现用例（NewWireValue(desc, hexDecode(rawHex)) 后重放同一路径）。
func recordWireMismatch(wv *WireValue, segs []string, detail string) {
	shadowMismatch.Add(1)
	name := wv.ProtoName()
	degradedSchemas.LoadOrStore(name, detail)
	dump := wv.raw
	truncated := false
	if len(dump) > mismatchDumpCap {
		dump = dump[:mismatchDumpCap]
		truncated = true
	}
	stresslog.Error("[WIRE] 影子验证失配，schema 降级回解码路径",
		zap.String("proto", name),
		zap.String("path", strings.Join(segs, ".")),
		zap.String("detail", detail),
		zap.Int("rawLen", len(wv.raw)),
		zap.Bool("rawTruncated", truncated),
		zap.String("rawHex", hex.EncodeToString(dump)))
}

// ReportWireFailure 把 wire 直转/扫描的**意外失败**按失配上报：记完整证据日志并
// 降级该 schema。供无采样保护的回退点调用（MaterializeValue / Lua 直转的 Walk
// 失败）——这些失败发生在 ValidateWire 已通过的字节上，必然是扫描器 bug，静默
// 回退会把 bug 藏起来；降级还能阻止同 schema 反复失败刷日志。
func (wv *WireValue) ReportWireFailure(stage string, err error) {
	if wv == nil || wv.desc == nil || err == nil {
		return
	}
	recordWireMismatch(wv, []string{stage}, "直转失败: "+err.Error())
}

// materializePlain 把导航产物物化为纯 Go 值（*WireValue/*Frozen → messageToMap 树），
// 供两侧逐字比对。容器递归复制，不改写入参。
func materializePlain(v any) any {
	switch x := v.(type) {
	case *WireValue:
		msg, err := x.Message()
		if err != nil {
			return "<wire-decode-error:" + err.Error() + ">"
		}
		return messageToMap(msg.ProtoReflect())
	case *Frozen:
		if x == nil || x.Message() == nil {
			return nil
		}
		return messageToMap(x.Message().ProtoReflect())
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = materializePlain(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = materializePlain(e)
		}
		return out
	default:
		return v
	}
}

// plainEqual 纯值深比较：[]byte 按内容（nil ≡ 空切片），容器递归，标量精确类型相等。
func plainEqual(a, b any) bool {
	if ab, ok := a.([]byte); ok {
		bb, ok2 := b.([]byte)
		return ok2 && bytes.Equal(ab, bb)
	}
	switch av := a.(type) {
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !plainEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			bvv, hit := bv[k]
			if !hit || !plainEqual(v, bvv) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

// summarizeValue 失配日志用的截断摘要。
func summarizeValue(v any) string {
	s := fmt.Sprintf("%T:%v", v, materializePlain(v))
	if len(s) > 512 {
		s = s[:512] + "...(truncated)"
	}
	return s
}

// WireShadowStats /debug/wire 输出的一次快照。
type WireShadowStats struct {
	Enabled    bool              `json:"enabled"`
	Navigates  int64             `json:"navigates"`
	Checks     int64             `json:"checks"`
	Mismatches int64             `json:"mismatches"`
	Degraded   map[string]string `json:"degraded"`
}

// SnapshotWireShadowStats 采集影子验证统计。
func SnapshotWireShadowStats() WireShadowStats {
	st := WireShadowStats{
		Enabled:    wireShadowOn.Load(),
		Navigates:  shadowNavigates.Load(),
		Checks:     shadowChecks.Load(),
		Mismatches: shadowMismatch.Load(),
		Degraded:   map[string]string{},
	}
	degradedSchemas.Range(func(k, v any) bool {
		st.Degraded[k.(string)] = v.(string)
		return true
	})
	return st
}

// resetWireShadowForTest 清空影子验证全局状态（仅测试用）。
func resetWireShadowForTest() {
	navResetAll()
	degradedSchemas = sync.Map{}
	shadowNavigates.Store(0)
	shadowChecks.Store(0)
	shadowMismatch.Store(0)
	wireShadowOn.Store(true)
}

func init() {
	// 与 /debug/dedup 同思路：挂 DefaultServeMux，启用 pprof 调试服务的进程自动获得。
	http.HandleFunc("/debug/wire", func(w http.ResponseWriter, _ *http.Request) {
		st := SnapshotWireShadowStats()
		// map 序列化本身无序；额外给出排序后的 schema 列表便于人读。
		names := make([]string, 0, len(st.Degraded))
		for k := range st.Degraded {
			names = append(names, k)
		}
		sort.Strings(names)
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			WireShadowStats
			DegradedList []string `json:"degradedList"`
		}{st, names})
	})
}
