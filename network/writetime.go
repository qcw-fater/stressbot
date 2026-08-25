// writetime.go 请求发送的「写完成时刻」登记，为 WireRTT 提供不受施压机负载影响的计时起点。
//
// 背景：AsyncWrite 只把数据挂进事件循环的待写队列就返回，真正的 write(2) 在事件循环后续
// 轮次里执行。旧实现把 AsyncWrite 返回的时刻当作 RTT 起点，于是「数据在待写队列里排了多久」
// 被整段算进服务端延迟——施压机 CPU 越忙这段越长，RTT 被系统性高估，Apdex 随之失真。
// gnet 的写完成回调给出数据真正交给内核的时刻，以它为起点，WireRTT 就只剩链路 + 服务端。
//
// 观测：/debug/rtt。

package network

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

// WriteDoneFunc 写完成回调：数据真正交给内核后，以写完成时刻调用。
type WriteDoneFunc func(at time.Time)

var (
	writeStampHits      atomic.Uint64 // 取到写完成时刻的次数
	writeStampFallbacks atomic.Uint64 // 回调未到、回退到入队时刻的次数
)

// writeStamp 一次请求发送的计时起点登记。
//
// 并发：mark 由事件循环 goroutine 调用，start 由业务 goroutine 调用，跨 goroutine 的
// 写完成时刻经原子传递；enqueuedAt 只由发起发送的那个 goroutine 写、同一 goroutine 读。
//
// 回调迟到（响应先于写回调被处理）时 start 回退到入队时刻。该回退只会让 RTT 偏大，
// 不会偏小——宁可高估，也不造出比真实更好看的数。回退次数计入 writeStampFallbacks，
// 占比过高说明写回调链路有问题，应当查而不是忍。
type writeStamp struct {
	enqueuedAt time.Time
	writtenNs  atomic.Int64
}

// mark 记录写完成时刻。作为 WriteDoneFunc 传给发送层。
func (ws *writeStamp) mark(at time.Time) {
	ws.writtenNs.Store(at.UnixNano())
}

// start 返回 WireRTT 的计时起点。
func (ws *writeStamp) start() time.Time {
	if ns := ws.writtenNs.Load(); ns > 0 {
		writeStampHits.Add(1)
		return time.Unix(0, ns)
	}
	writeStampFallbacks.Add(1)
	return ws.enqueuedAt
}

// WriteStampStats RTT 计时起点的取得情况。
type WriteStampStats struct {
	Hits      uint64  `json:"hits"`      // 用到真实写完成时刻的请求数
	Fallbacks uint64  `json:"fallbacks"` // 回退到入队时刻的请求数（RTT 偏大）
	HitRate   float64 `json:"hitRate"`   // Hits / (Hits+Fallbacks)
}

// SnapshotWriteStampStats 取当前计数快照。
func SnapshotWriteStampStats() WriteStampStats {
	st := WriteStampStats{
		Hits:      writeStampHits.Load(),
		Fallbacks: writeStampFallbacks.Load(),
	}
	if total := st.Hits + st.Fallbacks; total > 0 {
		st.HitRate = float64(st.Hits) / float64(total)
	}
	return st
}

func init() {
	// 与 /debug/dedup 同思路：挂 DefaultServeMux，启用 pprof 调试服务的进程自动获得。
	http.HandleFunc("/debug/rtt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			WriteStamp WriteStampStats `json:"writeStamp"`
		}{WriteStamp: SnapshotWriteStampStats()})
	})
}
