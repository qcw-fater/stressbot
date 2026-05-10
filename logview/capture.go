package logview

import (
	"fmt"

	"go.uber.org/zap/zapcore"
)

// captureCore 将日志追加到 RingBuffer，不影响原有 core 链。
type captureCore struct {
	ring   *RingBuffer
	fields []zapcore.Field
}

func (c *captureCore) Enabled(zapcore.Level) bool { return true }

func (c *captureCore) With(fields []zapcore.Field) zapcore.Core {
	return &captureCore{
		ring:   c.ring,
		fields: append(c.fields[:len(c.fields):len(c.fields)], fields...),
	}
}

func (c *captureCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	return ce.AddCore(ent, c)
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
