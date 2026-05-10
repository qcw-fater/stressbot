package logview

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// AttachRingBuffer 将环形缓冲区捕获 core 挂到给定 logger 上，
// 返回修改后的 logger（调用方需同步回 utils/log）。
// initialFields 为 logger 创建时通过 zap.Fields 注入的字段（如 SR），
// 这些字段在 zap.New 时已应用于原 core，captureCore 需要显式接收才能在 Write 时合并。
func AttachRingBuffer(logger *zap.Logger, size int, initialFields ...zap.Field) *zap.Logger {
	rb := NewRingBuffer(size)
	globalRingBuffer = rb
	cc := &captureCore{ring: rb}
	if len(initialFields) > 0 {
		fields := make([]zapcore.Field, len(initialFields))
		copy(fields, initialFields)
		cc = cc.With(fields).(*captureCore)
	}
	return logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return zapcore.NewTee(core, cc)
	}))
}

// GetRingBuffer 返回全局 RingBuffer（未 Attach 时为 nil）。
func GetRingBuffer() *RingBuffer {
	return globalRingBuffer
}

var globalRingBuffer *RingBuffer
