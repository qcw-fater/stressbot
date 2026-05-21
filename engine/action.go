package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"stressbot/adapter"
	"stressbot/errcode"
	"stressbot/protox"
	"stressbot/state"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// 声明式动作默认超时与轮询间隔。
const (
	DefaultRequestTimeoutSec = 10   // tcpRequest / udpRequest 默认超时（秒）
	DefaultListenTimeoutSec  = 60   // tcpListen / udpListen 默认超时（秒）
	DefaultPollMs            = 100  // 轮询间隔（毫秒）
	DefaultHeartbeatMs       = 3000 // 心跳默认间隔（毫秒）
)

// ActionExecutor 声明式动作执行器。
// 根据 ActionDef 的 Pattern 分派到具体的执行方法，处理消息构建、发送、接收和状态存储。
type ActionExecutor struct {
	netSender NetSender       // 网络发送委托，由 Robot 层实现
	store     *state.Store    // Robot 状态存储，保存服务器响应字段和中间变量
	factory   *protox.Factory // 动态 protobuf 消息工厂，用于创建/序列化/解析 proto 消息
	adp       adapter.Adapter // 协议适配器，处理消息头编解码和路由键计算
}

// NetSender 网络发送委托接口。
// 由 Robot 层实现，封装 TCP/UDP 连接管理和 HTTP 请求能力。
type NetSender interface {
	// ── 发送 / 请求-响应 ─────────────────────────────────────────────────

	// TCPSend 向指定服务发送 TCP 数据包（不等响应），返回发送字节数。
	TCPSend(service string, packet []byte) (int, error)
	// UDPSend 向指定服务发送 UDP 数据包（不等响应），返回发送字节数。
	UDPSend(service string, data []byte) (int, error)
	// TCPRequest 发送 TCP 请求并等待匹配 routeKey 的响应。
	// 返回响应体、协议头错误码和可能的错误。timeout 可选，覆盖默认超时。
	TCPRequest(service string, packet []byte, routeKey string, timeout ...time.Duration) (body []byte, headerErr uint64, err error)
	// UDPRequest 发送 UDP 请求并等待匹配 routeKey 的响应。
	// 返回值语义同 TCPRequest。
	UDPRequest(service string, packet []byte, routeKey string, timeout ...time.Duration) (body []byte, headerErr uint64, err error)
	// ConnectTCP 建立到指定地址的 TCP 连接，按 service 名注册。
	ConnectTCP(service, address string) error
	// ConnectUDP 建立到指定地址的 UDP 连接，按 service 名注册。
	ConnectUDP(service, address string) error
	// HTTPRequest 发送 HTTP 请求，返回状态码和响应体。
	HTTPRequest(url, method, contentType string, body []byte) (statusCode int, respBody []byte, err error)

	// ── 连接管理 ──────────────────────────────────────────────────────────

	// CloseTCP 关闭指定服务的 TCP 连接。
	CloseTCP(service string)
	// CloseUDP 关闭指定服务的 UDP 连接。
	CloseUDP(service string)

	// ── 监听 / 心跳 ────────────────────────────────────────────────────────

	// GetTCPListenResp 非阻塞获取 TCP 监听的最近一次响应（含协议头错误码）。
	GetTCPListenResp(service string, routeKey string) ([]byte, uint64)
	// GetUDPListenResp 非阻塞获取 UDP 监听的最近一次响应（含协议头错误码）。
	GetUDPListenResp(service string, routeKey string) ([]byte, uint64)
	// EnsureTCPListener 确保指定 routeKey 的 TCP 监听已注册。
	EnsureTCPListener(service string, routeKey string)
	// EnsureUDPListener 确保指定 routeKey 的 UDP 监听已注册。
	EnsureUDPListener(service string, routeKey string)
	// RegisterTCPHeartbeat 注册 TCP 心跳，按 intervalMs 间隔周期调用 builder 生成心跳包。
	RegisterTCPHeartbeat(service string, intervalMs int, builder func() []byte)
	// RegisterUDPHeartbeat 注册 UDP 心跳，按 intervalMs 间隔周期调用 builder 生成心跳包。
	RegisterUDPHeartbeat(service string, intervalMs int, builder func() []byte)

	// ── 加密密钥 ──────────────────────────────────────────────────────────

	// GetTCPSecretKey 获取指定服务 TCP 连接的加密密钥。
	GetTCPSecretKey(service string) []byte
	// SetTCPSecretKey 设置指定服务 TCP 连接的加密密钥。
	SetTCPSecretKey(service string, key []byte)
	// GetUDPSecretKey 获取指定服务 UDP 连接的加密密钥。
	GetUDPSecretKey(service string) []byte
	// SetUDPSecretKey 设置指定服务 UDP 连接的加密密钥。
	SetUDPSecretKey(service string, key []byte)
}

// NewActionExecutor 创建声明式动作执行器
func NewActionExecutor(store *state.Store, sender NetSender, factory *protox.Factory, adp adapter.Adapter) *ActionExecutor {
	return &ActionExecutor{
		netSender: sender,
		store:     store,
		factory:   factory,
		adp:       adp,
	}
}

// Execute 执行声明式动作，返回发送/接收字节数和错误。
func (ae *ActionExecutor) Execute(ctx context.Context, def *ActionDef) (sendBytes, recvBytes int, err error) {
	switch def.Pattern {
	case PatternTCPSend:
		sendBytes, err = ae.execSend("tcp", def)
	case PatternTCPRequest:
		sendBytes, recvBytes, err = ae.execRequest("tcp", def)
	case PatternTCPConnect:
		err = ae.execTCPConnect(def)
	case PatternTCPClose:
		err = ae.execTCPClose(def)
	case PatternTCPListen:
		recvBytes, err = ae.execListen(ctx, "tcp", def)
	case PatternUDPSend:
		sendBytes, err = ae.execSend("udp", def)
	case PatternUDPRequest:
		sendBytes, recvBytes, err = ae.execRequest("udp", def)
	case PatternUDPConnect:
		err = ae.execUDPConnect(def)
	case PatternUDPClose:
		err = ae.execUDPClose(def)
	case PatternUDPListen:
		recvBytes, err = ae.execListen(ctx, "udp", def)
	case PatternHTTPRequest:
		sendBytes, recvBytes, err = ae.execHTTPRequest(def)
	case PatternClearState:
		err = ae.execClearState(def)
	case PatternSetState:
		err = ae.execSetState(def)
	default:
		err = NewActionError(errcode.ErrUnknownPattern, "pattern="+def.Pattern)
	}
	return
}

// buildBody 构建消息体字节（序列化 proto 消息）。
func (ae *ActionExecutor) buildBody(def *ActionDef) ([]byte, error) {
	if def.C2SProto == "" {
		return nil, nil
	}
	msg, err := ae.factory.Create(def.C2SProto)
	if err != nil {
		return nil, NewActionError(errcode.ErrCreateMsg, "proto="+def.C2SProto, err)
	}
	if err := ae.bindFields(msg, def.Bindings, def.Name); err != nil {
		return nil, err
	}
	body, err := ae.factory.Serialize(msg)
	if err != nil {
		return nil, NewActionError(errcode.ErrSerialize, "action="+def.Name+" proto="+def.C2SProto, err)
	}
	return body, nil
}

// bindFields 将字段绑定列表应用到 proto 消息。
func (ae *ActionExecutor) bindFields(msg proto.Message, bindings []FieldBind, actionName string) error {
	for i := range bindings {
		fb := &bindings[i]

		if !ae.evaluateCondition(fb.Condition) {
			continue
		}

		value := ae.resolveFieldValue(fb)

		if value == nil {
			if fb.Optional {
				continue
			}
			if fb.Required || isImplicitRequired(fb.Type) {
				return NewActionError(errcode.ErrBindField, "action="+actionName+" field="+fb.Field)
			}
		}

		if fb.StoreAs != "" && value != nil {
			ae.store.Set(fb.StoreAs, value)
		}

		if value == nil || fb.Field == "" {
			continue
		}

		if err := ae.factory.SetField(msg, fb.Field, value); err != nil {
			return NewActionError(errcode.ErrBindField, "action="+actionName+" field="+fb.Field, err)
		}
	}
	return nil
}

// resolveFieldValue 解析字段绑定值，返回 Go 原生类型。
func (ae *ActionExecutor) resolveFieldValue(fb *FieldBind) any {
	var val any

	switch fb.Type {
	case BindFixed, "":
		val = fb.Value

	case BindState:
		if fb.Source == "" {
			return nil
		}
		val = ae.store.Get(fb.Source)

	case BindStateFirst:
		list := ae.store.GetList(fb.Source)
		if len(list) == 0 {
			return nil
		}
		val = list[0]

	case BindStateRandom:
		list := ae.store.GetList(fb.Source)
		if len(list) == 0 {
			return nil
		}
		filtered := ae.applyFilters(list, fb.Filters)
		if len(filtered) == 0 {
			return nil
		}
		val = filtered[rand.Intn(len(filtered))]

	case BindStateRandomN:
		list := ae.store.GetList(fb.Source)
		if len(list) == 0 {
			return nil
		}
		filtered := ae.applyFilters(list, fb.Filters)
		if len(filtered) == 0 {
			return nil
		}
		n := fb.Count
		if n <= 0 {
			n = 1
		}
		picked := pickN(filtered, n)
		if fb.Path != "" {
			result := make([]any, 0, len(picked))
			for _, item := range picked {
				v := navigatePath(item, fb.Path)
				if v != nil {
					result = append(result, v)
				}
			}
			return result
		}
		return picked

	case BindStateMapKey:
		m := ae.store.GetMap(fb.Source)
		if len(m) == 0 {
			return nil
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		return keys[rand.Intn(len(keys))]

	case BindStateMapValue:
		m := ae.store.GetMap(fb.Source)
		if len(m) == 0 {
			return nil
		}
		values := make([]any, 0, len(m))
		for _, v := range m {
			values = append(values, v)
		}
		if len(fb.Filters) > 0 {
			values = ae.applyFilters(values, fb.Filters)
		}
		if len(values) == 0 {
			return nil
		}
		val = values[rand.Intn(len(values))]

	case BindRandomPick:
		if len(fb.Values) == 0 {
			return nil
		}
		val = fb.Values[rand.Intn(len(fb.Values))]

	case BindRandomPickN:
		if len(fb.Values) == 0 {
			return nil
		}
		n := fb.Count
		if n <= 0 {
			n = 1
		}
		return pickN(fb.Values, n)

	case BindRandomPickMap:
		if len(fb.Values) == 0 || fb.KeySource == "" {
			return nil
		}
		keyVal := ae.store.Get(fb.KeySource)
		if keyVal == nil {
			return nil
		}
		keyStr := fmt.Sprintf("%v", keyVal)
		for _, entry := range fb.Values {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			k, _ := m["key"]
			if fmt.Sprintf("%v", k) != keyStr {
				continue
			}
			items, ok := m["values"].([]any)
			if !ok || len(items) == 0 {
				return nil
			}
			return items[rand.Intn(len(items))]
		}
		return nil

	case BindRandomExclude:
		var pool []any
		if len(fb.Values) > 0 {
			pool = fb.Values
		} else if fb.Source != "" {
			pool = ae.store.GetList(fb.Source)
		}
		if len(pool) == 0 {
			return nil
		}
		var exclude any
		if fb.ExcludeSource != "" {
			exclude = ae.store.Get(fb.ExcludeSource)
		}
		filtered := make([]any, 0, len(pool))
		for _, it := range pool {
			if !state.DeepEqual(it, exclude) {
				filtered = append(filtered, it)
			}
		}
		if len(filtered) == 0 {
			return nil
		}
		val = filtered[rand.Intn(len(filtered))]

	case BindRandomInt:
		lo, hi := fb.Min, fb.Max
		if lo >= hi {
			return lo
		}
		return rand.Intn(hi-lo+1) + lo

	case BindRandomFloat:
		lo, hi := float64(fb.Min), float64(fb.Max)
		if lo >= hi {
			return lo
		}
		prec := fb.Precision
		if prec <= 0 {
			prec = 2
		}
		v := lo + rand.Float64()*(hi-lo)
		scale := math.Pow10(prec)
		return math.Round(v*scale) / scale

	case BindRandomBool:
		return rand.Intn(2) == 1

	case BindRandomString:
		length := fb.Length
		if length <= 0 {
			length = 8
		}
		charset := fb.Charset
		if charset == "" {
			charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		}
		return randomStringCharset(length, charset)

	case BindListSize:
		return len(ae.store.GetList(fb.Source))

	default:
		stresslog.Debug("[ACTION] 未知 binding type，按 fixed 处理",
			zap.String("type", fb.Type), zap.String("field", fb.Field))
		val = fb.Value
	}

	if fb.Path != "" && val != nil {
		val = navigatePath(val, fb.Path)
	}

	if fb.Wrap && val != nil {
		return []any{val}
	}

	return val
}

// navigatePath 按点分路径从嵌套 map/list 中提取值。
func navigatePath(v any, path string) any {
	current := v
	for _, key := range state.SplitPath(path) {
		if current == nil {
			return nil
		}
		switch c := current.(type) {
		case map[string]any:
			current = c[key]
		case []any:
			idxStr := strings.Trim(key, "[]")
			var idx int
			_, err := fmt.Sscanf(idxStr, "%d", &idx)
			if err != nil || idx < 0 || idx >= len(c) {
				return nil
			}
			current = c[idx]
		default:
			return nil
		}
	}
	return current
}

// resolveAddress 解析 address 配置：支持 "state:key" 取 StateStore，否则按字面量
func (ae *ActionExecutor) resolveAddress(addr string) string {
	if strings.HasPrefix(addr, PrefixState) {
		key := addr[len(PrefixState):]
		return ae.store.GetString(key)
	}
	return addr
}

// execTCPConnect 建立 TCP 连接
func (ae *ActionExecutor) execTCPConnect(def *ActionDef) error {
	addr := ae.resolveAddress(def.Address)
	if addr == "" {
		return NewActionError(errcode.ErrAddrEmpty, "action="+def.Name+" service="+def.Service)
	}
	err := ae.netSender.ConnectTCP(def.Service, addr)
	if err != nil {
		return err
	}
	stresslog.Debug("[ACTION] ConnectTCP 成功", zap.String("action", def.Name), zap.String("service", def.Service), zap.String("address", addr))
	return nil
}

// execUDPConnect 建立 UDP 连接
func (ae *ActionExecutor) execUDPConnect(def *ActionDef) error {
	addr := ae.resolveAddress(def.Address)
	if addr == "" {
		return NewActionError(errcode.ErrAddrEmpty, "action="+def.Name+" service="+def.Service)
	}
	err := ae.netSender.ConnectUDP(def.Service, addr)
	if err != nil {
		return err
	}
	stresslog.Debug("[ACTION] ConnectUDP 成功", zap.String("action", def.Name), zap.String("service", def.Service), zap.String("address", addr))
	return nil
}

// execTCPClose 关闭 TCP 连接
func (ae *ActionExecutor) execTCPClose(def *ActionDef) error {
	ae.netSender.CloseTCP(def.Service)
	stresslog.Debug("[ACTION] TCPClose 成功", zap.String("action", def.Name), zap.String("service", def.Service))
	return nil
}

// execUDPClose 关闭 UDP 连接
func (ae *ActionExecutor) execUDPClose(def *ActionDef) error {
	ae.netSender.CloseUDP(def.Service)
	stresslog.Debug("[ACTION] UDPClose 成功", zap.String("action", def.Name), zap.String("service", def.Service))
	return nil
}

// execClearState 清除 StateStore 中的多个 key
func (ae *ActionExecutor) execClearState(def *ActionDef) error {
	for _, key := range def.Keys {
		ae.store.Delete(key)
	}
	stresslog.Debug("[ACTION] ClearState 成功", zap.String("action", def.Name), zap.Strings("keys", def.Keys))
	return nil
}

// execSetState 从 bindings 设置 StateStore
func (ae *ActionExecutor) execSetState(def *ActionDef) error {
	for i := range def.Bindings {
		fb := &def.Bindings[i]

		if !ae.evaluateCondition(fb.Condition) {
			continue
		}

		val := ae.resolveFieldValue(fb)
		if val == nil {
			continue
		}
		ae.store.Set(fb.Field, val)
	}
	return nil
}

// execHTTPRequest HTTP 请求
func (ae *ActionExecutor) execHTTPRequest(def *ActionDef) (int, int, error) {
	resolvedURL := ae.resolveAddress(def.URL)
	if resolvedURL == "" {
		return 0, 0, NewActionError(errcode.ErrURLEmpty, "action="+def.Name)
	}

	method := def.Method
	if method == "" {
		method = "POST"
	}
	contentType := def.ContentType
	if contentType == "" {
		contentType = ContentJSON
	}

	var body []byte
	if len(def.Bindings) > 0 {
		switch contentType {
		case ContentJSON:
			bodyMap := make(map[string]any)
			for i := range def.Bindings {
				fb := &def.Bindings[i]
				if !ae.evaluateCondition(fb.Condition) {
					continue
				}
				val := ae.resolveFieldValue(fb)
				if val == nil {
					continue
				}
				bodyMap[fb.Field] = val
			}
			var err error
			body, err = json.Marshal(bodyMap)
			if err != nil {
				return 0, 0, NewActionError(errcode.ErrMarshalBody, "action="+def.Name+" type=json", err)
			}
		case ContentForm:
			formData := make(map[string]string)
			for i := range def.Bindings {
				fb := &def.Bindings[i]
				if !ae.evaluateCondition(fb.Condition) {
					continue
				}
				val := ae.resolveFieldValue(fb)
				if val == nil {
					continue
				}
				formData[fb.Field] = fmt.Sprintf("%v", val)
			}
			var err error
			body, err = json.Marshal(formData)
			if err != nil {
				return 0, 0, NewActionError(errcode.ErrMarshalBody, "action="+def.Name+" type=form", err)
			}
		default:
			stresslog.Warn("[ACTION] 未知 contentType，将发送原始字节",
				zap.String("action", def.Name), zap.String("contentType", contentType))
		}
	}

	sendLen := len(resolvedURL) + len(body)
	statusCode, respBody, err := ae.netSender.HTTPRequest(resolvedURL, method, contentType, body)
	if err != nil {
		return sendLen, 0, err
	}

	if len(def.Store) > 0 && len(respBody) > 0 {
		if contentType == ContentJSON {
			var respMap map[string]any
			if err := json.Unmarshal(respBody, &respMap); err == nil {
				ae.storeResponse(def.Store, respMap)
			}
		}
	}

	stresslog.Debug("[ACTION] HTTPRequest 成功",
		zap.String("action", def.Name),
		zap.String("url", resolvedURL), zap.String("method", method),
		zap.Int("statusCode", statusCode),
		zap.Int("reqBodyLen", len(body)), zap.Int("respBodyLen", len(respBody)))
	return sendLen, len(respBody), nil
}

// parseAndStoreResponse 解析 S2C 响应消息并存储字段
func (ae *ActionExecutor) parseAndStoreResponse(def *ActionDef, respBody []byte) error {
	if len(def.Store) == 0 {
		return nil
	}

	if def.S2CProto == "" {
		for _, m := range def.Store {
			if m.Field == "" {
				ae.store.Set(m.Setter, respBody)
			}
		}
		return nil
	}

	respMsg, err := ae.factory.Parse(def.S2CProto, respBody)
	if err != nil {
		return NewActionError(errcode.ErrParseFailed, "action="+def.Name+" proto="+def.S2CProto, err)
	}

	fieldMap := ae.factory.GetFieldMap(respMsg)
	stresslog.Debug("[ACTION] TCPResponseProto", zap.String("proto", def.S2CProto), zap.Int("bodyLen", len(respBody)))

	ae.storeResponse(def.Store, fieldMap)
	return nil
}

// handleHeaderError 处理服务端返回的非零 headerErr：解析响应、构造错误描述。
// 将 4 处重复的 headerErr 处理逻辑统一收敛到此方法。
func (ae *ActionExecutor) handleHeaderError(def *ActionDef, headerErr uint64, routeKey string, respBody []byte) *ActionError {
	ae.parseAndStoreResponse(def, respBody)
	desc := ae.adp.DescribeError(headerErr)
	detail := "service=" + def.Service + " route=" + routeKey
	if desc != "" {
		detail = desc + ": " + detail
	}
	return NewServerError(headerErr, detail)
}

// storeResponse 将响应字段存储到 StateStore
func (ae *ActionExecutor) storeResponse(mappings []StoreMapping, fieldMap map[string]any) {
	stresslog.Debug("[ACTION] storeResponse",
		zap.Int("mappingCount", len(mappings)), zap.Int("fieldCount", len(fieldMap)))

	for _, m := range mappings {
		if m.Field == "" {
			ae.store.Set(m.Setter, fieldMap)
		} else {
			val := navigatePath(fieldMap, m.Field)
			if val != nil {
				ae.store.Set(m.Setter, val)
				stresslog.Debug("[ACTION] storeResponse 存储",
					zap.String("field", m.Field), zap.String("setter", m.Setter),
					zap.String("type", fmt.Sprintf("%T", val)))
			} else {
				stresslog.Debug("[ACTION] storeResponse 字段未找到",
					zap.String("field", m.Field), zap.String("setter", m.Setter))
			}
		}
	}
}

// applyFilters 按过滤条件筛选列表项
func (ae *ActionExecutor) applyFilters(list []any, filters []FilterDef) []any {
	if len(filters) == 0 {
		return list
	}
	result := make([]any, 0, len(list))
	for _, item := range list {
		if ae.matchFilters(item, filters) {
			result = append(result, item)
		}
	}
	return result
}

// matchFilters 检查列表项是否满足所有过滤条件
func (ae *ActionExecutor) matchFilters(item any, filters []FilterDef) bool {
	for _, f := range filters {
		var fieldVal any
		if f.Path == "" {
			fieldVal = item
		} else {
			fieldVal = navigatePath(item, f.Path)
		}

		var targetVal any
		if f.Source != "" {
			targetVal = ae.store.Get(f.Source)
		} else {
			targetVal = f.Value
		}

		if !state.CompareValues(fieldVal, targetVal, f.Op) {
			return false
		}
	}
	return true
}

// evaluateCondition 评估条件表达式（仅 state: 前缀）。返回 true 表示条件满足（或无条件）。
func (ae *ActionExecutor) evaluateCondition(cond string) bool {
	if cond == "" {
		return true
	}
	return EvalCondition(cond, ae.store)
}

// pickN 从列表中随机选择 N 个不重复元素
func pickN(list []any, n int) []any {
	if n >= len(list) {
		result := make([]any, len(list))
		copy(result, list)
		rand.Shuffle(len(result), func(i, j int) {
			result[i], result[j] = result[j], result[i]
		})
		return result
	}
	result := make([]any, 0, n)
	used := make(map[int]bool, n)
	for len(result) < n {
		idx := rand.Intn(len(list))
		if !used[idx] {
			used[idx] = true
			result = append(result, list[idx])
		}
	}
	return result
}

// ── 协议策略辅助方法 ─────────────────────────────────────────────────

// protocolSend sends a packet via TCP or UDP, returning send bytes.
func (ae *ActionExecutor) protocolSend(protocol, service string, packet []byte) (int, error) {
	if protocol == "udp" {
		return ae.netSender.UDPSend(service, packet)
	}
	return ae.netSender.TCPSend(service, packet)
}

// protocolEncode encodes a packet via TCP or UDP adapter.
func (ae *ActionExecutor) protocolEncode(protocol string, route any, body, key []byte) []byte {
	if protocol == "udp" {
		return ae.adp.EncodeUDP(route, body, key)
	}
	return ae.adp.EncodeTCP(route, body, key)
}

// protocolSecretKey returns the encryption key for the given protocol.
func (ae *ActionExecutor) protocolSecretKey(protocol, service string) []byte {
	if protocol == "udp" {
		return ae.netSender.GetUDPSecretKey(service)
	}
	return ae.netSender.GetTCPSecretKey(service)
}

// protocolRequest sends a request and waits for response.
func (ae *ActionExecutor) protocolRequest(protocol, service string, packet []byte, routeKey string, timeout ...time.Duration) (body []byte, headerErr uint64, err error) {
	if protocol == "udp" {
		return ae.netSender.UDPRequest(service, packet, routeKey, timeout...)
	}
	return ae.netSender.TCPRequest(service, packet, routeKey, timeout...)
}

// protocolEnsureListener ensures listener is registered.
func (ae *ActionExecutor) protocolEnsureListener(protocol, service, routeKey string) {
	if protocol == "udp" {
		ae.netSender.EnsureUDPListener(service, routeKey)
	} else {
		ae.netSender.EnsureTCPListener(service, routeKey)
	}
}

// protocolListenResp gets the latest listen response.
func (ae *ActionExecutor) protocolListenResp(protocol, service, routeKey string) ([]byte, uint64) {
	if protocol == "udp" {
		return ae.netSender.GetUDPListenResp(service, routeKey)
	}
	return ae.netSender.GetTCPListenResp(service, routeKey)
}

// ── 统一执行方法 ─────────────────────────────────────────────────────

// execSend sends a message without waiting for response.
func (ae *ActionExecutor) execSend(protocol string, def *ActionDef) (int, error) {
	body, err := ae.buildBody(def)
	if err != nil {
		return 0, err
	}

	routeKey := ae.adp.ExpectedRouteKey(def.Route)
	secretKey := ae.protocolSecretKey(protocol, def.Service)
	packet := ae.protocolEncode(protocol, def.Route, body, secretKey)
	if packet == nil {
		return 0, NewActionError(errcode.ErrEncodeFailed, "action="+def.Name+" route="+routeKey)
	}

	n, err := ae.protocolSend(protocol, def.Service, packet)
	if err != nil {
		return 0, err
	}

	label := "TCPSend"
	if protocol == "udp" {
		label = "UDPSend"
	}
	stresslog.Debug("[ACTION] "+label,
		zap.String("action", def.Name), zap.String("service", def.Service), zap.String("route", routeKey),
		zap.String("c2sProto", def.C2SProto), zap.Int("bodyLen", len(body)), zap.Int("pktLen", n))
	return len(packet), nil
}

// execRequest sends a request and waits for response.
func (ae *ActionExecutor) execRequest(protocol string, def *ActionDef) (int, int, error) {
	body, err := ae.buildBody(def)
	if err != nil {
		return 0, 0, err
	}

	routeKey := ae.adp.ExpectedRouteKey(def.Route)
	label := "TCPRequest"
	if protocol == "udp" {
		label = "UDPRequest"
	}
	stresslog.Debug("[ACTION] "+label+" 发送",
		zap.String("action", def.Name), zap.String("service", def.Service), zap.String("route", routeKey),
		zap.String("c2sProto", def.C2SProto), zap.String("s2cProto", def.S2CProto),
		zap.Int("bodyLen", len(body)))

	secretKey := ae.protocolSecretKey(protocol, def.Service)
	packet := ae.protocolEncode(protocol, def.Route, body, secretKey)
	if packet == nil {
		return 0, 0, NewActionError(errcode.ErrEncodeFailed, "action="+def.Name+" route="+routeKey)
	}

	start := time.Now()
	var reqTimeout []time.Duration
	if def.Timeout > 0 {
		reqTimeout = append(reqTimeout, time.Duration(def.Timeout)*time.Second)
	}
	respBody, headerErr, err := ae.protocolRequest(protocol, def.Service, packet, routeKey, reqTimeout...)
	elapsed := time.Since(start)
	if err != nil {
		return len(packet), 0, err
	}

	if headerErr != 0 {
		return len(packet), len(respBody), ae.handleHeaderError(def, headerErr, routeKey, respBody)
	}

	if err := ae.parseAndStoreResponse(def, respBody); err != nil {
		return len(packet), 0, err
	}

	stresslog.Debug("[ACTION] "+label+" 成功",
		zap.String("action", def.Name), zap.String("service", def.Service), zap.String("route", routeKey),
		zap.String("s2cProto", def.S2CProto),
		zap.Int("respBodyLen", len(respBody)), zap.Duration("elapsed", elapsed))
	return len(packet), len(respBody), nil
}

// execListen waits for a listen message (polling mode).
func (ae *ActionExecutor) execListen(ctx context.Context, protocol string, def *ActionDef) (int, error) {
	timeout := def.Timeout
	if timeout <= 0 {
		timeout = DefaultListenTimeoutSec
	}
	pollMs := def.PollMs
	if pollMs <= 0 {
		pollMs = DefaultPollMs
	}

	routeKey := ae.adp.ExpectedRouteKey(def.Route)
	label := "TCPListen"
	if protocol == "udp" {
		label = "UDPListen"
	}
	stresslog.Debug("[ACTION] "+label+" 开始",
		zap.String("action", def.Name), zap.String("service", def.Service), zap.String("route", routeKey),
		zap.String("s2cProto", def.S2CProto),
		zap.Int("timeoutSec", timeout))

	ae.protocolEnsureListener(protocol, def.Service, routeKey)

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	start := time.Now()
	pollCount := 0

	for time.Now().Before(deadline) {
		if ctx != nil && ctx.Err() != nil {
			return 0, ctx.Err()
		}
		pollCount++
		respBody, headerErr := ae.protocolListenResp(protocol, def.Service, routeKey)
		if respBody != nil {
			if headerErr != 0 {
				return 0, ae.handleHeaderError(def, headerErr, routeKey, respBody)
			}
			if err := ae.parseAndStoreResponse(def, respBody); err != nil {
				return 0, err
			}
			elapsed := time.Since(start)
			stresslog.Debug("[ACTION] "+label+" 成功",
				zap.String("action", def.Name), zap.String("service", def.Service), zap.String("route", routeKey),
				zap.String("s2cProto", def.S2CProto),
				zap.Int("respBodyLen", len(respBody)),
				zap.Int("pollCount", pollCount), zap.Duration("elapsed", elapsed))
			return len(respBody), nil
		}
		time.Sleep(time.Duration(pollMs) * time.Millisecond)
	}

	elapsed := time.Since(start)
	return 0, NewTimeoutError(errcode.ErrListenTimeout,
		"action="+def.Name+" service="+def.Service+" route="+routeKey+
			fmt.Sprintf(" timeout=%ds polls=%d elapsed=%v", timeout, pollCount, elapsed))
}

// randomStringCharset 生成指定字符集的随机字符串
func randomStringCharset(length int, charset string) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}
