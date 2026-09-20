package network

import (
	"errors"
	"testing"
	"time"
)

// TestPendingRequestRTTStartsAtWriteCompletion 验证 WireRTT 的起点是「数据真正交给内核」
// 的时刻，而不是 AsyncWrite 入队返回的时刻。
//
// 回归的是 RTT 被系统性高估的问题：AsyncWrite 只把数据挂进待写队列就返回，施压机越忙，
// 队列里排的时间越长，这段本属于客户端的延迟会被整段算进服务端 RTT。
func TestPendingRequestRTTStartsAtWriteCompletion(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	// 模拟事件循环：数据在待写队列里排了 50ms 才真正写出去。
	writtenAt := time.Now().Add(50 * time.Millisecond)
	conn.sendFunc = func(_ []byte, onWritten WriteDoneFunc) error {
		if onWritten != nil {
			onWritten(writtenAt)
		}
		return nil
	}

	const routeKey = "C2S.Probe"
	pr, err := conn.SendRequest(t.Context(), []byte("req"), routeKey, time.Second)
	if err != nil {
		t.Fatalf("SendRequest 失败: %v", err)
	}
	defer pr.Close()

	recvAt := writtenAt.Add(10 * time.Millisecond)
	conn.OnReceive(routeKey, []byte("resp"), 0, 4, MessageTiming{
		RecvFrameAt: recvAt, DecodeStart: recvAt, DecodeEnd: recvAt, DispatchStart: recvAt,
	})

	select {
	case resp := <-pr.C():
		got := pr.Timing(resp).WireRTT
		if got != 10*time.Millisecond {
			t.Fatalf("WireRTT = %v, want 10ms（若为 ~60ms 说明仍以入队时刻为起点）", got)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到响应")
	}
}

// TestPendingRequestRTTFallsBackWhenWriteCallbackMissing 写完成回调没到时回退到入队时刻，
// 并计入 fallbacks。回退只会让 RTT 偏大，不会造出比真实更好看的数。
func TestPendingRequestRTTFallsBackWhenWriteCallbackMissing(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	// 不调 onWritten，模拟回调迟到 / 底层不支持。
	conn.sendFunc = func(_ []byte, _ WriteDoneFunc) error { return nil }

	before := SnapshotWriteStampStats()

	const routeKey = "C2S.NoCallback"
	pr, err := conn.SendRequest(t.Context(), []byte("req"), routeKey, time.Second)
	if err != nil {
		t.Fatalf("SendRequest 失败: %v", err)
	}
	defer pr.Close()

	recvAt := time.Now().Add(20 * time.Millisecond)
	conn.OnReceive(routeKey, []byte("resp"), 0, 4, MessageTiming{
		RecvFrameAt: recvAt, DecodeStart: recvAt, DecodeEnd: recvAt, DispatchStart: recvAt,
	})

	select {
	case resp := <-pr.C():
		if got := pr.Timing(resp).WireRTT; got <= 0 {
			t.Fatalf("WireRTT = %v, want > 0（应回退到入队时刻）", got)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到响应")
	}

	after := SnapshotWriteStampStats()
	if after.Fallbacks <= before.Fallbacks {
		t.Fatalf("Fallbacks 未增加: before=%d after=%d", before.Fallbacks, after.Fallbacks)
	}
}

// TestWriteDoneCallbackIgnoresFailedWrite 写失败时不登记写完成时刻。
//
// gnet 在异步写失败时也会带 err 调用写完成回调。此时数据并没有交给内核，若照样登记
// 时间戳，一次失败的发送就会凭空产出一个 RTT 起点，污染 WireRTT 与 Apdex。
func TestWriteDoneCallbackIgnoresFailedWrite(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	var ws writeStamp
	ws.enqueuedAt = time.Now()

	cb := writeDoneCallback(conn, ws.mark)
	if err := cb(nil, errors.New("wsasend: 连接被对端重置")); err != nil {
		t.Fatalf("回调不应向 gnet 返回错误: %v", err)
	}
	if got := ws.writtenNs.Load(); got != 0 {
		t.Fatalf("写失败仍登记了写完成时刻: %d", got)
	}

	// 写成功时照常登记。
	if err := cb(nil, nil); err != nil {
		t.Fatalf("回调不应向 gnet 返回错误: %v", err)
	}
	if ws.writtenNs.Load() == 0 {
		t.Fatal("写成功应登记写完成时刻")
	}
}

// TestSendWithoutWriteStampSkipsCallback 即发即忘路径不挂写完成回调——
// 这条路径每秒十万级，不能为不需要的 RTT 起点多付一个闭包分配。
func TestSendWithoutWriteStampSkipsCallback(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	var gotCallback bool
	conn.sendFunc = func(_ []byte, onWritten WriteDoneFunc) error {
		gotCallback = onWritten != nil
		return nil
	}

	if _, err := conn.Send([]byte("fire-and-forget")); err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if gotCallback {
		t.Fatal("Send 不应传入写完成回调")
	}
}
