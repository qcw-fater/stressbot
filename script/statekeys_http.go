package script

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"

	"stressbot/protox"
	"stressbot/state"
)

// ── state key 访问观测端点 ─────────────────────────────────────
//
// /debug/statekeys 输出 robot.get / robot.get_path 的按 key 调用计数，用于定位
// "高频整读大 key"热点（CPU 剖面只能看到 goValueToLua 的总量，看不到 key 名）。
// 三个计数的判读：
//   - calls：调用次数；
//   - tables：返回值需要构建 Lua 表（容器转换，成本 ∝ 树大小）；
//   - wireDecodes：容器来自 WireValue/Overlay 等 wire 形态（转换前还要先解码）。
// calls 高 + tables 高的 key 就是整读热点；wireDecodes ≈ tables 说明该 key 走
// wire-first 留存、直转器（D1）收益直接作用于它。
//
// 计数为全进程聚合（跨全部机器人），atomic 自增零锁；key 集合由脚本字面量决定、
// 天然有界，防御性上限 4096 之后归入 __overflow__。?reset=1 读后清零（分窗口对比）。
//
// 默认关闭（?enable=1 打开，?enable=0 关闭）。
// 关闭的理由是实测数据而非洁癖：埋点要按 kind+":"+name 现拼 map 键，这次拼接是一次
// 堆分配，摊在 robot.get / get_path 每一次调用上。基准（BenchmarkLuaAPI_*，每次调用）
// 显示 get 比只有调用前缀的 get_id 多出整整 1 次分配，差值即此。10000 机器人稳态下
// 这些分配同时吃 CPU 和 GC，而它只是一个定位热点用的诊断工具——需要时打开即可，
// 不该让常态压测替它买单。

const stateKeyMaxTracked = 4096

// stateKeyTracking 埋点总开关。默认关闭，经 /debug/statekeys?enable=1 打开。
var stateKeyTracking atomic.Bool

type stateKeyStat struct {
	calls       atomic.Uint64
	tables      atomic.Uint64
	wireDecodes atomic.Uint64
}

var (
	stateKeyStats sync.Map // string → *stateKeyStat
	stateKeyCount atomic.Int64
)

// stateKeyStatFor 取/建 key 的计数槽；超过跟踪上限归入该 kind 的 __overflow__。
func stateKeyStatFor(kind, name string) *stateKeyStat {
	key := kind + ":" + name
	v, ok := stateKeyStats.Load(key)
	if !ok {
		if stateKeyCount.Load() >= stateKeyMaxTracked {
			key = kind + ":__overflow__"
			if v, ok = stateKeyStats.Load(key); !ok {
				v, _ = stateKeyStats.LoadOrStore(key, &stateKeyStat{})
			}
		} else {
			var loaded bool
			v, loaded = stateKeyStats.LoadOrStore(key, &stateKeyStat{})
			if !loaded {
				stateKeyCount.Add(1)
			}
		}
	}
	return v.(*stateKeyStat)
}

// recordStateKeyGet 埋点：robot.get(kind="get", name=key) / robot.get_path(kind="get_path", name=path)。
// val 为 Store 返回的原始值（转换前），按类型归类成本。
func recordStateKeyGet(kind, name string, val any) {
	if !stateKeyTracking.Load() {
		return
	}
	st := stateKeyStatFor(kind, name)
	st.calls.Add(1)
	switch val.(type) {
	case nil, bool, int, int32, int64, uint, uint32, uint64, float32, float64, string:
		return
	case *protox.WireValue:
		st.tables.Add(1)
		st.wireDecodes.Add(1)
	case state.ValueMaterializer:
		st.tables.Add(1)
		st.wireDecodes.Add(1)
	default:
		// map/slice/Frozen 等容器形态：建表但无 wire 解码。
		st.tables.Add(1)
	}
}

// recordStateKeyView 埋点：robot.get_view(key)。视图零物化，只计 calls——
// 复测时对照整读计数即可验证"该走视图的 key 是否已迁移"。
func recordStateKeyView(name string) {
	if !stateKeyTracking.Load() {
		return
	}
	stateKeyStatFor("view", name).calls.Add(1)
}

type stateKeyRow struct {
	Key         string `json:"key"`
	Calls       uint64 `json:"calls"`
	Tables      uint64 `json:"tables"`
	WireDecodes uint64 `json:"wireDecodes"`
}

// stateKeyReport 端点响应。带上 enabled 是必要的：埋点默认关闭，
// 若只返回空数组，读者无法区分「没开埋点」和「真的没有 key 访问」。
type stateKeyReport struct {
	Enabled bool          `json:"enabled"`
	Rows    []stateKeyRow `json:"rows"`
}

func init() {
	http.HandleFunc("/debug/statekeys", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch q.Get("enable") {
		case "1":
			stateKeyTracking.Store(true)
		case "0":
			stateKeyTracking.Store(false)
		}

		reset := q.Get("reset") == "1"
		rows := make([]stateKeyRow, 0, 64)
		stateKeyStats.Range(func(k, v any) bool {
			st := v.(*stateKeyStat)
			var row stateKeyRow
			row.Key = k.(string)
			if reset {
				row.Calls = st.calls.Swap(0)
				row.Tables = st.tables.Swap(0)
				row.WireDecodes = st.wireDecodes.Swap(0)
			} else {
				row.Calls = st.calls.Load()
				row.Tables = st.tables.Load()
				row.WireDecodes = st.wireDecodes.Load()
			}
			if row.Calls > 0 {
				rows = append(rows, row)
			}
			return true
		})
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Tables != rows[j].Tables {
				return rows[i].Tables > rows[j].Tables
			}
			return rows[i].Calls > rows[j].Calls
		})
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(stateKeyReport{Enabled: stateKeyTracking.Load(), Rows: rows})
	})
}
