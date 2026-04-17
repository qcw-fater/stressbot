package network

// Message 网络消息，包含消息头信息和消息体数据。
// 消息头的编解码由 Protocol 处理，Message 只持有解码后的结构化字段。
type Message struct {
	Head *HeadDecode
	Data []byte
}

// NewMessage 创建新消息
func NewMessage(head *HeadDecode, data []byte) *Message {
	return &Message{Head: head, Data: data}
}

// CmdAct 返回消息的路由键
func (m *Message) CmdAct() int {
	if m.Head == nil {
		return 0
	}
	return m.Head.CmdAct
}

// Cmd 返回 CMD 值
func (m *Message) Cmd() uint8 {
	if m.Head == nil {
		return 0
	}
	return m.Head.Cmd
}

// Act 返回 ACT 值
func (m *Message) Act() uint8 {
	if m.Head == nil {
		return 0
	}
	return m.Head.Act
}

// Error 返回错误码
func (m *Message) Error() uint16 {
	if m.Head == nil {
		return 0
	}
	return m.Head.Error
}

// Copy 深拷贝消息
func (m *Message) Copy() *Message {
	if m == nil {
		return nil
	}
	var data []byte
	if m.Data != nil {
		data = make([]byte, len(m.Data))
		copy(data, m.Data)
	}
	var head *HeadDecode
	if m.Head != nil {
		h := *m.Head
		head = &h
	}
	return &Message{Head: head, Data: data}
}
