package network

import "time"

// MessageTiming 入站消息的网络层计时点。
type MessageTiming struct {
	RecvFrameAt   time.Time
	DecodeStart   time.Time
	DecodeEnd     time.Time
	DispatchStart time.Time
}

// RequestTiming 单次 request-response 的网络层耗时。
type RequestTiming struct {
	SendCost             time.Duration
	WireRTT              time.Duration
	DecodeWait           time.Duration
	DecodeCost           time.Duration
	DispatchToActionWait time.Duration
}

// Message 网络消息。
type Message struct {
	RouteKey  string        // 路由键字符串（由 adapter.Decode 产生）
	Data      []byte        // 消息体字节
	HeaderErr uint64        // 协议头错误码，0 表示无错误
	Timing    MessageTiming // 入站计时点
}

// NewMessage 创建新消息
func NewMessage(routeKey string, data []byte, headerErr uint64, timing MessageTiming) *Message {
	return &Message{RouteKey: routeKey, Data: data, HeaderErr: headerErr, Timing: timing}
}

// Copy 深拷贝消息
func (m *Message) Copy() *Message {
	if m == nil {
		return nil
	}
	data := make([]byte, len(m.Data))
	copy(data, m.Data)
	return &Message{RouteKey: m.RouteKey, Data: data, HeaderErr: m.HeaderErr, Timing: m.Timing}
}

func safeSub(end, start time.Time) time.Duration {
	if end.IsZero() || start.IsZero() {
		return 0
	}
	d := end.Sub(start)
	if d < 0 {
		return 0
	}
	return d
}
