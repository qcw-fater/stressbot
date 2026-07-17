package logview

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// AttachRingBuffer 将环形缓冲区捕获 core 挂到给定 logger 上，
// 返回修改后的 logger（调用方需同步回 utils/log）。
// enab 为等级过滤器（通常传入 utils/log 的全局 AtomicLevel），使环形缓冲区与
// 文件/控制台 core 使用同一等级门槛；传 nil 则捕获全部等级。
// initialFields 为 logger 创建时通过 zap.Fields 注入的字段（如 SR），
// 这些字段在 zap.New 时已应用于原 core，captureCore 需要显式接收才能在 Write 时合并。
func AttachRingBuffer(logger *zap.Logger, size int, enab zapcore.LevelEnabler, initialFields ...zap.Field) *zap.Logger {
	rb := NewRingBuffer(size)
	globalRingBuffer = rb
	cc := &captureCore{ring: rb, enab: enab}
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
