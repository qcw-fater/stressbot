// Package protocol 提供协议适配器接口和声明式 codec 适配器包装。
// 所有消息编解码、帧分割、路由键提取都通过 Adapter 接口。
// Go 引擎只调用此接口，不感知具体协议格式。
package protocol

// Adapter 协议适配器接口。
// 所有消息编解码、帧分割、路由键提取都通过此接口。
// Go 引擎只调用此接口，不感知具体协议格式。
type Adapter interface {
	// ─── 帧分割（热路径纯 Go）────────────────────────────────────────────

	// HeaderSize 返回消息头固定字节数。
	HeaderSize() int

	// BodyLength 从消息头字节中解析消息体长度。
	// 此方法在 gnet 热路径中被每包调用，必须是无阻塞、无脚本调用的纯计算。
	BodyLength(headerData []byte) int

	// ─── 编解码 ───────────────────────────────────────────────────────

	// EncodeTCP 将路由信息+消息体编码为完整 TCP 数据包（含消息头）。
	// route 为不透明类型，由 flow.json 中声明，原样传给具体 Adapter 实现。
	// secretKey 为连接加密密钥，nil 表示不加密。
	// route 为 nil 时，适配器应视为"无路由请求"（如密钥交换）。
	EncodeTCP(route any, body []byte, secretKey []byte) []byte

	// EncodeUDP 将路由信息+消息体编码为 UDP 数据包。
	// 与 EncodeTCP 的区别：内部应用 UDP 加密偏移量（前 N 字节保持明文，
	// 供服务端通过明文头部查找密钥表）。偏移值来自声明式 codec 配置，Go 层通过 Adapter 统一调用。
	// route 为 nil 时行为同 EncodeTCP。
	EncodeUDP(route any, body []byte, secretKey []byte) []byte

	// DecodeTCP 将 TCP 数据包解码为路由键、消息体和协议头错误码。
	// routeKey 是字符串路由键，用于请求-响应匹配和监听分发。
	// 格式由适配器决定，典型格式："{cmd}:{act}"，如 "3:1"。
	// headerErr 为协议头错误码；非零表示服务端业务失败，调用方负责记录并进入 action onError 链路。
	DecodeTCP(data []byte, secretKey []byte) (routeKey string, body []byte, headerErr uint64)

	// DecodeUDP 将 UDP 数据包解码为路由键、消息体和协议头错误码。
	// 与 DecodeTCP 分离，允许适配器对 TCP/UDP 使用不同的解码策略。
	DecodeUDP(data []byte, secretKey []byte) (routeKey string, body []byte, headerErr uint64)

	// ExpectedRouteKey 从发送路由计算期望的响应路由键。
	// 用于 TCPRequest 等待响应时注册临时通道。
	ExpectedRouteKey(route any) string

	// Close 释放适配器持有的资源；无资源实现可保持 no-op。
	Close()

	// DescribeError 将服务端协议头错误码映射为可读描述。
	// 可选功能：由共享 errors.json 提供，未命中返回空字符串。
	DescribeError(code uint64) string
}
