package logview

import (
	"fmt"

	"go.uber.org/zap/zapcore"
)

// captureCore 将日志追加到 RingBuffer，不影响原有 core 链。
type captureCore struct {
	ring *RingBuffer
}

func (c *captureCore) Enabled(zapcore.Level) bool { return true }

func (c *captureCore) With([]zapcore.Field) zapcore.Core { return c }

func (c *captureCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	return ce.AddCore(ent, c)
}

func (c *captureCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	service := ""
	for _, f := range fields {
		if f.Key == "SR" {
			service = fmt.Sprintf("%v", f.Interface)
			if service == "" {
				service = f.String
			}
			break
		}
	}

	c.ring.Append(ent.Level.String(), ent.Time, ent.Caller.TrimmedPath(), ent.Message, service, fields)
	return nil
}

func (c *captureCore) Sync() error { return nil }

var _ zapcore.Core = (*captureCore)(nil)
