// Package network 提供基于 gnet 的高性能网络层。
// 支持可配置的消息头协议、TCP/UDP 连接、RequestResponse 同步等待模式。
// 消息头格式通过 header.json 配置，适配不同游戏服务器的协议。
package network

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"

	"go.uber.org/zap"
	stresslog "stressbot/utils/log"
)

// HeaderFieldDef 消息头字段定义，从 header.json 加载
type HeaderFieldDef struct {
	Name   string `json:"name"`
	Type   string `json:"type"` // uint8/uint16/uint32/int32/int64/float32/float64
	Offset int    `json:"offset"`
}

// EncryptConfig 加密配置
type EncryptConfig struct {
	Type      string `json:"type"`      // "xor" 等
	KeyField  string `json:"keyField"`  // 校验字段名
	Offset    int    `json:"offset"`    // TCP 加密偏移量（默认 0）
	UDPOffset int    `json:"udpOffset"` // UDP 发送加密偏移量（默认 11，明文前 11 字节保留：PacketIndex u16 + BattleId i64 + FighterIndex u8，供服务端查表找密钥）
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
	RoutingFields []string         `json:"routingFields"` // 用于路由的字段名（如 cmd, act）
	LengthField   string           `json:"lengthField"`   // 消息体长度字段名
	ErrorField    string           `json:"errorField"`    // 错误码字段名
	FlagsField    string           `json:"flagsField"`    // 标志位字段名
	EncryptFlag   *FlagBitConfig   `json:"encryptFlag"`   // 加密标志位
	CompressFlag  *FlagBitConfig   `json:"compressFlag"`  // 压缩标志位
	Encrypt       *EncryptConfig   `json:"encrypt"`       // 加密配置
	HeartbeatCmd  uint8            `json:"heartbeatCmd"`  // 心跳 CMD
	HeartbeatAct  uint8            `json:"heartbeatAct"`  // 心跳 ACT
}

// Protocol 消息头编解码器
type Protocol struct {
	config     ProtocolConfig
	byteOrder  binary.ByteOrder
	fieldMap   map[string]*HeaderFieldDef
	cmdField   *HeaderFieldDef
	actField   *HeaderFieldDef
	lenField   *HeaderFieldDef
	errField   *HeaderFieldDef
	flagsField *HeaderFieldDef
}

// NewProtocol 从配置创建协议编解码器
func NewProtocol(cfg ProtocolConfig) *Protocol {
	// 为 UDP 加密偏移量提供默认值（与服务端 UDP 头部对齐：
	// 2+8+1 = 11 字节明文：PacketIndex u16 | BattleId i64 | FighterIndex u8）
	if cfg.Encrypt != nil && cfg.Encrypt.UDPOffset == 0 {
		cfg.Encrypt.UDPOffset = 11
	}
	p := &Protocol{
		config:   cfg,
		fieldMap: make(map[string]*HeaderFieldDef),
	}

	// 设置字节序
	if cfg.ByteOrder == "big" {
		p.byteOrder = binary.BigEndian
	} else {
		p.byteOrder = binary.LittleEndian
	}

	// 建立字段索引
	for i := range cfg.Fields {
		f := &cfg.Fields[i]
		p.fieldMap[f.Name] = f
	}

	// 缓存关键字段
	if len(cfg.RoutingFields) >= 2 {
		p.cmdField = p.fieldMap[cfg.RoutingFields[0]]
		p.actField = p.fieldMap[cfg.RoutingFields[1]]
	}
	p.lenField = p.fieldMap[cfg.LengthField]
	p.errField = p.fieldMap[cfg.ErrorField]
	if cfg.FlagsField != "" {
		p.flagsField = p.fieldMap[cfg.FlagsField]
	}

	return p
}

// HeadSize 返回消息头大小
func (p *Protocol) HeadSize() int {
	return p.config.Size
}

// UDPEncryptOffset 返回 UDP 发送时的加密偏移量（body[0:offset] 保持明文以便服务端查表找密钥）。
// 未配置加密时返回 0。
func (p *Protocol) UDPEncryptOffset() int {
	if p.config.Encrypt == nil {
		return 0
	}
	return p.config.Encrypt.UDPOffset
}

// Config 返回协议配置（只读访问）
func (p *Protocol) Config() ProtocolConfig {
	return p.config
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
	Bcc    uint8
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

// IsEncrypted 检查标志位是否设置了加密
func (p *Protocol) IsEncrypted(flags uint8) bool {
	if p.config.EncryptFlag == nil {
		return false
	}
	return flags&(1<<p.config.EncryptFlag.Bit) != 0
}

// SetEncryptFlag 设置加密标志位
func (p *Protocol) SetEncryptFlag(flags uint8) uint8 {
	if p.config.EncryptFlag == nil {
		return flags
	}
	return flags | (1 << p.config.EncryptFlag.Bit)
}

// IsCompressed 检查标志位是否设置了压缩
func (p *Protocol) IsCompressed(flags uint8) bool {
	if p.config.CompressFlag == nil {
		return false
	}
	return flags&(1<<p.config.CompressFlag.Bit) != 0
}

// EncryptBody 加密消息体（从起始位置加密，等价于 offset=0）。
// 使用与服务端一致的加密算法：(byte ^ key) + carry, rotate left 3
func (p *Protocol) EncryptBody(body []byte, key []byte, bcc uint8) []byte {
	return p.EncryptBodyWithOffset(body, key, 0)
}

// EncryptBodyWithOffset 支持偏移量的加密。
// body[0:offset] 保持明文（通常是服务端查找密钥需要的路由头，如 UDP 的 BattleId/FighterIndex），
// body[offset:] 按照与服务端 DefaultNetEncrypt 一致的算法加密（key 下标从 0 开始）。
func (p *Protocol) EncryptBodyWithOffset(body []byte, key []byte, offset int) []byte {
	if p.config.Encrypt == nil {
		return body
	}
	if len(body) == 0 || len(key) == 0 {
		return body
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(body) {
		offset = len(body)
	}
	result := make([]byte, len(body))
	copy(result[:offset], body[:offset])
	var c byte
	for i := offset; i < len(body); i++ {
		k := i - offset
		mask := key[k&31]
		x := (body[i] ^ mask) + c
		x = (x << 3) | (x >> 5)
		c = x
		result[i] = c
	}
	return result
}

// DecryptBody 解密消息体，与 EncryptBody 互逆（offset=0）。
func (p *Protocol) DecryptBody(body []byte, key []byte, bcc uint8) []byte {
	return p.DecryptBodyWithOffset(body, key, 0)
}

// DecryptBodyWithOffset 支持偏移量的解密，与 EncryptBodyWithOffset 互逆。
func (p *Protocol) DecryptBodyWithOffset(body []byte, key []byte, offset int) []byte {
	if p.config.Encrypt == nil {
		return body
	}
	if len(body) == 0 || len(key) == 0 {
		return body
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(body) {
		offset = len(body)
	}
	result := make([]byte, len(body))
	copy(result[:offset], body[:offset])
	var c byte
	for i := offset; i < len(body); i++ {
		k := i - offset
		mask := key[k&31]
		x := body[i]
		x = (x >> 3) | (x << 5)
		x = (x - c) ^ mask
		c = body[i]
		result[i] = x
	}
	return result
}

// DecompressBody gzip 解压消息体。
// 服务端对 >= 2048 字节的消息体做 gzip 压缩后发送。
func (p *Protocol) DecompressBody(body []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gzip 解压失败: %w", err)
	}
	defer reader.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(reader); err != nil {
		return nil, fmt.Errorf("gzip 读取失败: %w", err)
	}
	return buf.Bytes(), nil
}

// ComputeBCC 计算 BCC 校验值（异或校验，offset=0）。
func (p *Protocol) ComputeBCC(data []byte) uint8 {
	return p.ComputeBCCWithOffset(data, 0)
}

// ComputeBCCWithOffset 计算 BCC（从 offset 起异或 data[offset:]）。
// 与服务端 DefaultNetEncrypt/Decrypt 的 v 累加完全一致：只对加密区域做 XOR。
func (p *Protocol) ComputeBCCWithOffset(data []byte, offset int) uint8 {
	if offset < 0 {
		offset = 0
	}
	if offset > len(data) {
		return 0
	}
	var bcc uint8
	for i := offset; i < len(data); i++ {
		bcc ^= data[i]
	}
	return bcc
}

// BuildPacket 构建完整的加密报文（消息头 + 加密消息体，offset=0）。
// 整合 EncodeHead + 加密标志设置 + BCC 计算 + 消息体加密。
// secretKey 为通信加密密钥（32 字节）。
func (p *Protocol) BuildPacket(cmd, act uint8, body []byte, secretKey []byte) []byte {
	return p.BuildPacketWithOffset(cmd, act, body, secretKey, 0)
}

// BuildPacketWithOffset 构建报文，支持加密偏移量。
// encryptOffset > 0 时，body[0:encryptOffset] 保持明文（用于 UDP 路由头场景：
// 服务端需从明文前 11 字节读取 BattleId/FighterIndex 以查找解密密钥）。
// 当 secretKey 为空或长度为 0 时，退化为不加密。
func (p *Protocol) BuildPacketWithOffset(cmd, act uint8, body []byte, secretKey []byte, encryptOffset int) []byte {
	var flags uint8

	var bcc uint8
	encryptedBody := body
	if p.config.Encrypt != nil && len(body) > 0 && len(secretKey) > 0 {
		// BCC 仅对加密区域异或，与服务端 DefaultNetDecrypt 返回的 v 保持一致
		bcc = p.ComputeBCCWithOffset(body, encryptOffset)
		encryptedBody = p.EncryptBodyWithOffset(body, secretKey, encryptOffset)
		flags = p.SetEncryptFlag(flags)
	}

	head := p.EncodeHead(cmd, act, uint32(len(encryptedBody)), flags)

	if p.config.Encrypt != nil && len(body) > 0 && len(secretKey) > 0 && p.config.Encrypt.KeyField != "" {
		bccField := p.fieldMap[p.config.Encrypt.KeyField]
		if bccField != nil {
			head[bccField.Offset] = bcc
		}
	}

	packet := make([]byte, 0, len(head)+len(encryptedBody))
	packet = append(packet, head...)
	packet = append(packet, encryptedBody...)

	return packet
}

// DecodePacket 解码完整报文（消息头解密 + 消息体解密）。
// 返回解码后的消息头和解密后的消息体。
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

	// 检查是否需要解密
	if p.IsEncrypted(head.Flags) && p.config.Encrypt != nil && len(secretKey) > 0 {
		// 获取 BCC 值
		var bcc uint8
		if p.config.Encrypt.KeyField != "" {
			bccField := p.fieldMap[p.config.Encrypt.KeyField]
			if bccField != nil {
				bcc = uint8(p.getFieldBuf(headData, bccField))
			}
		}
		body = p.DecryptBody(body, secretKey, bcc)
	}

	// 检查是否需要解压（gzip）
	if p.IsCompressed(head.Flags) {
		decompressed, err := p.DecompressBody(body)
		if err != nil {
			stresslog.Error("[PROTOCOL] gzip 解压失败", zap.Error(err), zap.Int("bodyLen", len(body)))
			return head, body
		}
		body = decompressed
	}

	return head, body
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
	return fmt.Sprintf("Protocol(size=%d, order=%s, fields=%d)", p.config.Size, p.config.ByteOrder, len(p.config.Fields))
}
