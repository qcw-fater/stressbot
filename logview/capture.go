package logview

import (
	"fmt"

	"go.uber.org/zap/zapcore"
)

// captureCore 将日志追加到 RingBuffer，不影响原有 core 链。
// enab 为可选的等级过滤器（通常为 utils/log 的全局 AtomicLevel），
// nil 时捕获全部等级；非 nil 时与文件/控制台 core 使用同一等级门槛，
// 避免环形缓冲区（前端实时日志面板数据源）捕获到已被全局等级过滤掉的日志。
type captureCore struct {
	ring   *RingBuffer
	enab   zapcore.LevelEnabler
	fields []zapcore.Field
}

func (c *captureCore) Enabled(lvl zapcore.Level) bool {
	return c.enab == nil || c.enab.Enabled(lvl)
}

func (c *captureCore) With(fields []zapcore.Field) zapcore.Core {
	return &captureCore{
		ring:   c.ring,
		enab:   c.enab,
		fields: append(c.fields[:len(c.fields):len(c.fields)], fields...),
	}
}

func (c *captureCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *captureCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	merged := fields
	if len(c.fields) > 0 {
		merged = make([]zapcore.Field, 0, len(c.fields)+len(fields))
		merged = append(merged, c.fields...)
		merged = append(merged, fields...)
	}

	service := ""
	for _, f := range merged {
		if f.Key == "SR" {
			if f.String != "" {
				service = f.String
			} else {
				service = fmt.Sprintf("%v", f.Interface)
			}
			break
		}
	}

	c.ring.Append(ent.Level.String(), ent.Time, ent.Caller.TrimmedPath(), ent.Message, service, merged)
	return nil
}

func (c *captureCore) Sync() error { return nil }

var _ zapcore.Core = (*captureCore)(nil)
