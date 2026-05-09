package logview

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// AttachRingBuffer 将环形缓冲区捕获 core 挂到给定 logger 上，
// 返回修改后的 logger（调用方需同步回 utils/log）。
func AttachRingBuffer(logger *zap.Logger, size int) *zap.Logger {
	rb := NewRingBuffer(size)
	globalRingBuffer = rb
	cc := &captureCore{ring: rb}
	return logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return zapcore.NewTee(core, cc)
	}))
}

// GetRingBuffer 返回全局 RingBuffer（未 Attach 时为 nil）。
func GetRingBuffer() *RingBuffer {
	return globalRingBuffer
}

var globalRingBuffer *RingBuffer
