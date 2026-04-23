// Package adapter 提供协议适配器接口和 Lua 桥接实现。
// 所有消息编解码、帧分割、路由键提取都通过 Adapter 接口。
// Go 引擎只调用此接口，不感知具体协议格式。
package adapter

// Adapter 协议适配器接口。
// 所有消息编解码、帧分割、路由键提取都通过此接口。
// Go 引擎只调用此接口，不感知具体协议格式。
type Adapter interface {
	// ─── 帧分割（纯 Go 实现，无 Lua 调用）───────────────────────────────

	// HeaderSize 返回消息头固定字节数。
	// 初始化时从 Lua 缓存，运行时无 Lua 调用。
	HeaderSize() int

	// BodyLength 从消息头字节中解析消息体长度。
	// 初始化时从 Lua 获取字段偏移/类型元信息后，在 Go 层原生实现。
	// 此方法在 gnet 热路径中被每包调用，禁止进行任何 Lua 调用。
	BodyLength(headerData []byte) int

	// ─── 编解码（Lua 调用）──────────────────────────────────────────────

	// Encode 将路由信息+消息体编码为完整 TCP 数据包（含消息头）。
	// route 为不透明类型，由 flow.json 中声明，原样传给 Lua。
	// secretKey 为连接加密密钥，nil 表示不加密。
	// route 为 nil 时，适配器应视为"无路由请求"（如密钥交换 cmd=0,act=0）。
	Encode(route any, body []byte, secretKey []byte) []byte

	// EncodeUDP 将路由信息+消息体编码为 UDP 数据包。
	// 与 Encode 的区别：内部应用 UDP 加密偏移量（前 N 字节保持明文，
	// 供服务端通过明文头部查找密钥表）。偏移值由 codec.lua 内部定义，Go 层无需知晓。
	// route 为 nil 时行为同 Encode。
	EncodeUDP(route any, body []byte, secretKey []byte) []byte

	// Decode 将完整数据包解码为路由键、消息体和协议头错误码。
	// responseKey 是字符串路由键，用于请求-响应匹配和监听分发。
	// 格式由适配器决定，典型格式："{cmd}:{act}"，如 "3:1"。
	// headerErr 为协议头中的错误码，Lua decode() 必须返回数字，Go 用 uint64 接收。
	// 非零时 Connection.OnReceive 记录告警，仍继续路由（让请求正常完成）。
	// TCP 和 UDP 使用同一 Decode（接收侧无偏移问题）。
	Decode(data []byte, secretKey []byte) (responseKey string, body []byte, headerErr uint64)

	// ExpectedResponseKey 从发送路由计算期望的响应路由键。
	// 用于 TCPRequest 等待响应时注册临时通道。
	ExpectedResponseKey(route any) string
}
