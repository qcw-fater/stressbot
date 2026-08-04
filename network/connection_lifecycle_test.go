package network

import (
	"sync/atomic"
	"testing"
)

func TestConnectionLifecycleDeliversLateCallbacksOnce(t *testing.T) {
	connection := &Connection{}
	connection.publishDisconnected()
	connection.publishClosed()

	var disconnected atomic.Int64
	var closed atomic.Int64
	connection.SetOnDisconnect(func() { disconnected.Add(1) })
	connection.SetOnClosed(func() { closed.Add(1) })
	connection.SetOnDisconnect(func() { disconnected.Add(1) })
	connection.SetOnClosed(func() { closed.Add(1) })
	connection.publishDisconnected()
	connection.publishClosed()

	if got := disconnected.Load(); got != 1 {
		t.Fatalf("断开回调次数 = %d, want 1", got)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("关闭回调次数 = %d, want 1", got)
	}
}
