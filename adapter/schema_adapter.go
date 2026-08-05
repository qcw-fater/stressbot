// Package adapter — SchemaAdapter：声明式 codec 引擎（codec.SchemaCodec）的 Adapter 薄包装。
//
// codec/ 包提供 encode + decode + 访问器 + DescribeError 全套，本文件把它们组装成现成的
// adapter.Adapter 实现。
//
// 设计要点：
//   - 仅 import codec 包（**不 import gopher-lua**）——生产代码无 Lua 依赖。
//   - 9 方法逐字匹配 adapter.Adapter 接口；编译期断言 var _ Adapter = (*SchemaAdapter)(nil)。
//   - LoadSchema / LoadErrorMap 由调用方（loader）先做，NewSchemaAdapter 收 *CodecSchema + errorMap。
//   - Close 是幂等 no-op（codec.SchemaCodec 无资源需释放，编译产物无锁无状态）。
package adapter

import (
	"stressbot/codec"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// SchemaAdapter 把 *codec.SchemaCodec 包装为 adapter.Adapter。
//
// 持有编译期不可变的 *codec.SchemaCodec，任意 goroutine 并发调用 9 方法无需加锁
// （codec 兑现 invariant 2：无可变字段）。
type SchemaAdapter struct {
	c *codec.SchemaCodec
}

// 编译期接口断言：SchemaAdapter 必须实现 adapter.Adapter 全 9 方法，签名逐字一致。
var _ Adapter = (*SchemaAdapter)(nil)

// NewSchemaAdapter 编译 *codec.CodecSchema 并包装为 Adapter。
//
// 入参：
//   - schema：已 LoadSchema 得到的 *CodecSchema（Validate 由 NewSchemaCodec 内部再调一次）。
//   - errorMap：已 LoadErrorMap 得到的 code→desc 映射；nil 视为空 map（DescribeError 永远返回空串）。
//
// 失败（schema 非法 / 算法缺失 / 引用悬空等）返回 error，调用方应放弃该 codec 切换。
func NewSchemaAdapter(schema *codec.CodecSchema, errorMap map[uint64]string) (Adapter, error) {
	sc, err := codec.NewSchemaCodec(schema, errorMap)
	if err != nil {
		return nil, err
	}
	return &SchemaAdapter{c: sc}, nil
}

// HeaderSize 返回消息头字节数（codec.SchemaCodec.HeaderSize 委托）。
func (a *SchemaAdapter) HeaderSize() int { return a.c.HeaderSize() }

// BodyLength 从 header 字节解析 body 长度（codec.SchemaCodec.BodyLength 委托）。
func (a *SchemaAdapter) BodyLength(headerData []byte) int { return a.c.BodyLength(headerData) }

// FrameSignature 返回底层 codec 的帧切割签名（codec.SchemaCodec.FrameSignature 委托）。
// 供 LoadCodecResolver 校验多连接 codec 帧规则一致（gnet 单一元信息源前提）。
func (a *SchemaAdapter) FrameSignature() codec.FrameSignature { return a.c.FrameSignature() }

// EncodeTCP 编码 TCP 数据包（codec.SchemaCodec.EncodeTCP 委托）。
func (a *SchemaAdapter) EncodeTCP(route any, body, secretKey []byte) []byte {
	return a.c.EncodeTCP(route, body, secretKey)
}

// EncodeUDP 编码 UDP 数据包（codec.SchemaCodec.EncodeUDP 委托）。
func (a *SchemaAdapter) EncodeUDP(route any, body, secretKey []byte) []byte {
	return a.c.EncodeUDP(route, body, secretKey)
}

// DecodeTCP 解码 TCP 数据包（codec.SchemaCodec.DecodeTCPWithReason 委托）。
// 解码步骤按 onError=fail 中止时 reason 非空，打一条 Warn 便于追踪（区分帧不完整与解密/解压/校验失败）。
func (a *SchemaAdapter) DecodeTCP(data, secretKey []byte) (string, []byte, uint64) {
	routeKey, body, headerErr, reason := a.c.DecodeTCPWithReason(data, secretKey)
	if reason != "" {
		stresslog.Warn("[CODEC] TCP 解码失败",
			zap.String("reason", reason),
			zap.Int("dataLen", len(data)),
			zap.Uint64("headerErr", headerErr))
	}
	return routeKey, body, headerErr
}

// DecodeUDP 解码 UDP 数据包（codec.SchemaCodec.DecodeUDPWithReason 委托）。语义同 DecodeTCP。
func (a *SchemaAdapter) DecodeUDP(data, secretKey []byte) (string, []byte, uint64) {
	routeKey, body, headerErr, reason := a.c.DecodeUDPWithReason(data, secretKey)
	if reason != "" {
		stresslog.Warn("[CODEC] UDP 解码失败",
			zap.String("reason", reason),
			zap.Int("dataLen", len(data)),
			zap.Uint64("headerErr", headerErr))
	}
	return routeKey, body, headerErr
}

// ExpectedRouteKey 计算响应路由键（codec.SchemaCodec.ExpectedRouteKey 委托）。
func (a *SchemaAdapter) ExpectedRouteKey(route any) string { return a.c.ExpectedRouteKey(route) }

// DescribeError 错误码→中文描述（codec.SchemaCodec.DescribeError 委托）。
func (a *SchemaAdapter) DescribeError(code uint64) string { return a.c.DescribeError(code) }

// Close 释放资源。codec.SchemaCodec 无可释放资源（编译产物无锁无状态），幂等 no-op。
func (a *SchemaAdapter) Close() {}
