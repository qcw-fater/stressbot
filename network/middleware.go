package network

// PacketDirection 报文方向
type PacketDirection int

const (
	PacketSend PacketDirection = iota // 发送（Build 方向）
	PacketRecv                        // 接收（Decode 方向）
)

// PacketContext 中间件上下文，贯穿整个中间件链。
// 中间件通过读取和修改此结构来处理报文。
type PacketContext struct {
	Direction     PacketDirection // Send 或 Recv
	Cmd           uint8           // 路由 CMD
	Act           uint8           // 路由 ACT
	Body          []byte          // 可变报文体（中间件直接修改）
	Head          []byte          // 可变消息头字节（通过 SetHeaderField/GetHeaderField 读写）
	Flags         uint8           // 标志位（中间件可设置/读取）
	SecretKey     []byte          // 连接加密密钥（32 字节，可能为 nil）
	EncryptOffset int             // UDP 加密偏移量（body[0:offset] 保持明文）

	proto *Protocol // 内部引用，提供 header field 访问
	err   error     // 链错误
}

// SetHeaderField 写入指定名称的 header 字段。
func (ctx *PacketContext) SetHeaderField(name string, value uint64) {
	if ctx.proto == nil || ctx.Head == nil {
		return
	}
	field := ctx.proto.fieldMap[name]
	if field == nil {
		return
	}
	ctx.proto.setFieldBuf(ctx.Head, field, value)
}

// GetHeaderField 读取指定名称的 header 字段值。
func (ctx *PacketContext) GetHeaderField(name string) uint64 {
	if ctx.proto == nil || ctx.Head == nil {
		return 0
	}
	field := ctx.proto.fieldMap[name]
	if field == nil {
		return 0
	}
	return ctx.proto.getFieldBuf(ctx.Head, field)
}

// SetFlag 设置标志位（指定 bit 位置 1）。
func (ctx *PacketContext) SetFlag(bit int) {
	if bit >= 0 && bit < 8 {
		ctx.Flags |= 1 << bit
	}
}

// HasFlag 检查标志位是否设置。
func (ctx *PacketContext) HasFlag(bit int) bool {
	if bit < 0 || bit > 7 {
		return false
	}
	return ctx.Flags&(1<<bit) != 0
}

// SetError 记录错误，链将停止执行。
func (ctx *PacketContext) SetError(err error) {
	ctx.err = err
}

// Error 返回链中的错误。
func (ctx *PacketContext) Error() error {
	return ctx.err
}

// PacketMiddleware 中间件函数签名。
// ctx 携带报文状态，next() 调用下一个中间件。
// 必须调用 next() 以继续链；不调用则短路（剩余中间件不执行）。
type PacketMiddleware func(ctx *PacketContext, next func())
