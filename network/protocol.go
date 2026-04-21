// Package network 提供基于 gnet 的高性能网络层。
// 支持可配置的消息头协议、TCP/UDP 连接、RequestResponse 同步等待模式。
// 消息头格式通过 header.json 配置，加密/压缩/校验通过中间件链实现可插拔。
package network

import (
	"encoding/binary"
	"fmt"
)

// HeaderFieldDef 消息头字段定义，从 header.json 加载
type HeaderFieldDef struct {
	Name   string `json:"name"`
	Type   string `json:"type"` // uint8/uint16/uint32/int32/int64/float32/float64
	Offset int    `json:"offset"`
}

// EncryptConfig 加密配置
type EncryptConfig struct {
	Type      string `json:"type"`      // "xor" 等（用于自动选择中间件）
	KeyField  string `json:"keyField"`  // 校验字段名
	Offset    int    `json:"offset"`    // TCP 加密偏移量（默认 0）
	UDPOffset int    `json:"udpOffset"` // UDP 发送加密偏移量（默认 11）
}

// FlagBitConfig 标志位配置
type FlagBitConfig struct {
	Bit  int    `json:"bit"`
	Name string `json:"name"`
}

// ProtocolConfig 消息头协议配置，从 header.json 加载
type ProtocolConfig struct {
	Size          int              `json:"size"`
	ByteOrder     string           `json:"byteOrder"` // "little" 或 "big"
	Fields        []HeaderFieldDef `json:"fields"`
	RoutingFields []string         `json:"routingFields"`
	LengthField   string           `json:"lengthField"`
	ErrorField    string           `json:"errorField"`
	FlagsField    string           `json:"flagsField"`
	EncryptFlag   *FlagBitConfig   `json:"encryptFlag"`
	CompressFlag  *FlagBitConfig   `json:"compressFlag"`
	Encrypt       *EncryptConfig   `json:"encrypt"`
	HeartbeatCmd  uint8            `json:"heartbeatCmd"`
	HeartbeatAct  uint8            `json:"heartbeatAct"`
	Middleware    []string         `json:"middleware"` // 命名中间件列表（可选，不填则自动注册）
}

// Protocol 消息头编解码器。
// 头部帧格式通过 header.json 配置，加密/压缩/校验通过中间件链处理。
type Protocol struct {
	config     ProtocolConfig
	byteOrder  binary.ByteOrder
	fieldMap   map[string]*HeaderFieldDef
	cmdField   *HeaderFieldDef
	actField   *HeaderFieldDef
	lenField   *HeaderFieldDef
	errField   *HeaderFieldDef
	flagsField *HeaderFieldDef

	middlewares []PacketMiddleware // 中间件链
}

// NewProtocol 从配置创建协议编解码器。
// 自动根据配置注册内置中间件（BCC/XOR/GZIP），保持向后兼容。
func NewProtocol(cfg ProtocolConfig) *Protocol {
	if cfg.Encrypt != nil && cfg.Encrypt.UDPOffset == 0 {
		cfg.Encrypt.UDPOffset = 11
	}
	p := &Protocol{
		config:   cfg,
		fieldMap: make(map[string]*HeaderFieldDef),
	}

	if cfg.ByteOrder == "big" {
		p.byteOrder = binary.BigEndian
	} else {
		p.byteOrder = binary.LittleEndian
	}

	for i := range cfg.Fields {
		f := &cfg.Fields[i]
		p.fieldMap[f.Name] = f
	}

	if len(cfg.RoutingFields) >= 2 {
		p.cmdField = p.fieldMap[cfg.RoutingFields[0]]
		p.actField = p.fieldMap[cfg.RoutingFields[1]]
	}
	p.lenField = p.fieldMap[cfg.LengthField]
	p.errField = p.fieldMap[cfg.ErrorField]
	if cfg.FlagsField != "" {
		p.flagsField = p.fieldMap[cfg.FlagsField]
	}

	p.resolveConfigMiddleware()

	return p
}

// Use 注册中间件到链中。
// Send 和 Recv 都按注册顺序执行，每个中间件根据 ctx.Direction 决定行为。
// 必须在首次 BuildPacket/DecodePacket 调用之前注册。
func (p *Protocol) Use(mw ...PacketMiddleware) {
	p.middlewares = append(p.middlewares, mw...)
}

// resolveConfigMiddleware 根据 header.json 的 middleware 数组解析并注册中间件。
// 用户需在调用 NewProtocol 之前通过 RegisterMiddleware 注册自定义中间件工厂。
// 若 middleware 数组为空，则不注册任何中间件。
func (p *Protocol) resolveConfigMiddleware() {
	for _, name := range p.config.Middleware {
		p.Use(resolveMiddleware(name, p.config))
	}
}

// HeadSize 返回消息头大小
func (p *Protocol) HeadSize() int {
	return p.config.Size
}

// UDPEncryptOffset 返回 UDP 发送时的加密偏移量。
func (p *Protocol) UDPEncryptOffset() int {
	if p.config.Encrypt == nil {
		return 0
	}
	return p.config.Encrypt.UDPOffset
}

// CmdAct 计算消息路由键
func (p *Protocol) CmdAct(cmd, act uint8) int {
	return int(cmd)<<8 + int(act)
}

// EncodeHead 编码消息头
func (p *Protocol) EncodeHead(cmd, act uint8, bodyLen uint32, flags uint8) []byte {
	buf := make([]byte, p.config.Size)
	p.setFieldBuf(buf, p.cmdField, uint64(cmd))
	p.setFieldBuf(buf, p.actField, uint64(act))
	p.setFieldBuf(buf, p.lenField, uint64(bodyLen))
	if p.flagsField != nil {
		p.setFieldBuf(buf, p.flagsField, uint64(flags))
	}
	return buf
}

// HeadDecode 消息头解码结果
type HeadDecode struct {
	Cmd    uint8
	Act    uint8
	Len    uint32
	Error  uint16
	Flags  uint8
	CmdAct int
}

// DecodeHead 解码消息头
func (p *Protocol) DecodeHead(data []byte) *HeadDecode {
	if len(data) < p.config.Size {
		return nil
	}
	h := &HeadDecode{}
	if p.cmdField != nil {
		h.Cmd = uint8(p.getFieldBuf(data, p.cmdField))
	}
	if p.actField != nil {
		h.Act = uint8(p.getFieldBuf(data, p.actField))
	}
	if p.lenField != nil {
		h.Len = uint32(p.getFieldBuf(data, p.lenField))
	}
	if p.errField != nil {
		h.Error = uint16(p.getFieldBuf(data, p.errField))
	}
	if p.flagsField != nil {
		h.Flags = uint8(p.getFieldBuf(data, p.flagsField))
	}
	h.CmdAct = p.CmdAct(h.Cmd, h.Act)
	return h
}

// BuildPacket 构建报文（offset=0）。
func (p *Protocol) BuildPacket(cmd, act uint8, body []byte, secretKey []byte) []byte {
	return p.BuildPacketWithOffset(cmd, act, body, secretKey, 0)
}

// BuildPacketWithOffset 构建报文，支持加密偏移量。
// 先设置 header 基础字段，再运行中间件链处理 body，最后更新 header 中的 flags 和 len。
func (p *Protocol) BuildPacketWithOffset(cmd, act uint8, body []byte, secretKey []byte, encryptOffset int) []byte {
	// 预分配 header
	head := make([]byte, p.config.Size)
	p.setFieldBuf(head, p.cmdField, uint64(cmd))
	p.setFieldBuf(head, p.actField, uint64(act))

	// 复制 body 以避免修改原始数据
	var bodyCopy []byte
	if len(body) > 0 {
		bodyCopy = make([]byte, len(body))
		copy(bodyCopy, body)
	}

	ctx := &PacketContext{
		Direction:     PacketSend,
		Cmd:           cmd,
		Act:           act,
		Body:          bodyCopy,
		Head:          head,
		Flags:         0,
		SecretKey:     secretKey,
		EncryptOffset: encryptOffset,
		proto:         p,
	}

	// 运行中间件链（同序）
	p.runChain(ctx)

	// 更新 header 中的 flags 和 body 长度
	if p.flagsField != nil {
		p.setFieldBuf(head, p.flagsField, uint64(ctx.Flags))
	}
	p.setFieldBuf(head, p.lenField, uint64(len(ctx.Body)))

	// 组装报文
	packet := make([]byte, 0, len(head)+len(ctx.Body))
	packet = append(packet, head...)
	packet = append(packet, ctx.Body...)
	return packet
}

// DecodePacket 解码完整报文。
// 先解析 header，再运行中间件链处理 body。
func (p *Protocol) DecodePacket(data []byte, secretKey []byte) (*HeadDecode, []byte) {
	if len(data) < p.config.Size {
		return nil, nil
	}

	headData := data[:p.config.Size]
	head := p.DecodeHead(headData)
	if head == nil {
		return nil, nil
	}

	body := data[p.config.Size:]
	if len(body) == 0 {
		return head, body
	}

	// 复制 body 以避免修改原始数据
	bodyCopy := make([]byte, len(body))
	copy(bodyCopy, body)

	ctx := &PacketContext{
		Direction: PacketRecv,
		Cmd:       head.Cmd,
		Act:       head.Act,
		Body:      bodyCopy,
		Head:      headData,
		Flags:     head.Flags,
		SecretKey: secretKey,
		proto:     p,
	}

	// 运行中间件链（同序）
	p.runChain(ctx)

	return head, ctx.Body
}

// runChain 按注册顺序执行中间件链。
// Send 和 Recv 都使用相同顺序，每个中间件根据 ctx.Direction 决定行为。
func (p *Protocol) runChain(ctx *PacketContext) {
	if len(p.middlewares) == 0 {
		return
	}
	idx := 0
	var next func()
	next = func() {
		if idx >= len(p.middlewares) || ctx.err != nil {
			return
		}
		mw := p.middlewares[idx]
		idx++
		mw(ctx, next)
	}
	next()
}

func (p *Protocol) setFieldBuf(buf []byte, field *HeaderFieldDef, value uint64) {
	if field == nil {
		return
	}
	off := field.Offset
	switch field.Type {
	case "uint8":
		buf[off] = byte(value)
	case "uint16":
		p.byteOrder.PutUint16(buf[off:], uint16(value))
	case "uint32":
		p.byteOrder.PutUint32(buf[off:], uint32(value))
	case "int32":
		p.byteOrder.PutUint32(buf[off:], uint32(value))
	case "int64", "uint64":
		p.byteOrder.PutUint64(buf[off:], value)
	}
}

func (p *Protocol) getFieldBuf(data []byte, field *HeaderFieldDef) uint64 {
	if field == nil {
		return 0
	}
	off := field.Offset
	switch field.Type {
	case "uint8":
		return uint64(data[off])
	case "uint16":
		return uint64(p.byteOrder.Uint16(data[off:]))
	case "uint32":
		return uint64(p.byteOrder.Uint32(data[off:]))
	case "int32":
		return uint64(p.byteOrder.Uint32(data[off:]))
	case "int64", "uint64":
		return p.byteOrder.Uint64(data[off:])
	default:
		return uint64(data[off])
	}
}

// String 返回协议信息
func (p *Protocol) String() string {
	return fmt.Sprintf("Protocol(size=%d, order=%s, fields=%d, middlewares=%d)",
		p.config.Size, p.config.ByteOrder, len(p.config.Fields), len(p.middlewares))
}
