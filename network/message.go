package network

// Message 网络消息。
type Message struct {
	ResponseKey string // 路由键字符串（由 adapter.Decode 产生）
	Data        []byte // 消息体字节
	HeaderErr   uint64 // 协议头错误码，0 表示无错误
}

// NewMessage 创建新消息
func NewMessage(responseKey string, data []byte, headerErr uint64) *Message {
	return &Message{ResponseKey: responseKey, Data: data, HeaderErr: headerErr}
}

// Copy 深拷贝消息
func (m *Message) Copy() *Message {
	if m == nil {
		return nil
	}
	data := make([]byte, len(m.Data))
	copy(data, m.Data)
	return &Message{ResponseKey: m.ResponseKey, Data: data, HeaderErr: m.HeaderErr}
}
