package network

// Message 网络消息。
type Message struct {
	ResponseKey string // 路由键字符串（由 adapter.Decode 产生）
	Data        []byte // 消息体字节
}

// NewMessage 创建新消息
func NewMessage(responseKey string, data []byte) *Message {
	return &Message{ResponseKey: responseKey, Data: data}
}

// Copy 深拷贝消息
func (m *Message) Copy() *Message {
	if m == nil {
		return nil
	}
	data := make([]byte, len(m.Data))
	copy(data, m.Data)
	return &Message{ResponseKey: m.ResponseKey, Data: data}
}
