package network

import "testing"

// EngineConfig 归一化契约：零值全默认；显式正值照办；非正值（0/负数）按未配置回退默认。
func TestEngineConfigNormalize(t *testing.T) {
	t.Run("零值全默认", func(t *testing.T) {
		got := EngineConfig{}.normalize()
		if got.readBufferCap != defaultReadBufferCap || got.writeBufferCap != defaultWriteBufferCap ||
			got.maxConcurrentDials != defaultMaxDials || got.maxBodyLen != defaultMaxBodyLen {
			t.Fatalf("默认值不符合: %+v", got)
		}
		if got.numEventLoop < 1 {
			t.Fatalf("numEventLoop 默认应为 NumCPU()，得到 %d", got.numEventLoop)
		}
	})

	t.Run("显式正值照办", func(t *testing.T) {
		got := EngineConfig{
			ReadBufferCap:      64 * 1024,
			WriteBufferCap:     16 * 1024,
			NumEventLoop:       4,
			MaxConcurrentDials: 128,
			MaxBodyLen:         1024,
		}.normalize()
		if got.readBufferCap != 64*1024 || got.writeBufferCap != 16*1024 || got.numEventLoop != 4 ||
			got.maxConcurrentDials != 128 || got.maxBodyLen != 1024 {
			t.Fatalf("显式值未生效: %+v", got)
		}
	})

	t.Run("非正值按未配置回退默认", func(t *testing.T) {
		got := EngineConfig{
			ReadBufferCap:      -1,
			WriteBufferCap:     0,
			NumEventLoop:       -2,
			MaxConcurrentDials: 0,
			MaxBodyLen:         -8,
		}.normalize()
		if got.readBufferCap != defaultReadBufferCap || got.writeBufferCap != defaultWriteBufferCap ||
			got.maxConcurrentDials != defaultMaxDials || got.maxBodyLen != defaultMaxBodyLen {
			t.Fatalf("非正值应回退默认: %+v", got)
		}
	})
}
