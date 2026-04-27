package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"stressbot/adapter"
	"stressbot/protox"
	"stressbot/state"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// ErrActionSkip 动作跳过信号：必填字段为空时触发。
var ErrActionSkip = errors.New("action skipped: required field is nil")

// ActionExecutor 声明式动作执行器。
type ActionExecutor struct {
	netSender NetSender       // 网络发送委托
	store     *state.Store    // Robot 状态存储
	factory   *protox.Factory // 动态消息工厂
	adp       adapter.Adapter // 协议适配器
	ctx       context.Context // 用于长轮询中检测取消
}

// NetSender 网络发送委托接口。
type NetSender interface {
	TCPSend(service string, packet []byte) (bool, int)
	TCPRequest(service string, packet []byte, responseKey string) ([]byte, bool)
	HTTPPost(path string, formData map[string]string) (statusCode int, body []byte, err error)
	UDPSend(service string, data []byte) (bool, int)
	ConnectTCP(service, address string) bool
	ConnectUDP(service, address string) bool
	CloseTCP(service string)
	CloseUDP(service string)
	GetTCPListenResp(service string, responseKey string) []byte
	GetUDPListenResp(service string, responseKey string) []byte
	GetTCPSecretKey(service string) []byte
	SetTCPSecretKey(service string, key []byte)
	SetUDPSecretKey(service string, key []byte)
	GetUDPSecretKey(service string) []byte
	EnsureTCPListener(service string, responseKey string)
	EnsureUDPListener(service string, responseKey string)
	RegisterTCPHeartbeat(service string, intervalMs int, builder func() []byte)
	RegisterUDPHeartbeat(service string, intervalMs int, builder func() []byte)
}

// NewActionExecutor 创建声明式动作执行器
func NewActionExecutor(store *state.Store, sender NetSender, factory *protox.Factory, adp adapter.Adapter, ctx context.Context) *ActionExecutor {
	return &ActionExecutor{
		netSender: sender,
		store:     store,
		factory:   factory,
		adp:       adp,
		ctx:       ctx,
	}
}

// Execute 执行声明式动作
func (ae *ActionExecutor) Execute(def *ActionDef) error {
	var err error
	switch def.Pattern {
	case PatternTCPSend:
		err = ae.execTCPSend(def)
	case PatternTCPRequest:
		err = ae.execTCPRequest(def)
	case PatternConnect:
		err = ae.execConnect(def)
	case PatternConnectUDP:
		err = ae.execConnectUDP(def)
	case PatternWaitListen:
		err = ae.execWaitListen(def)
	case PatternExchangeKey:
		err = ae.execExchangeKey(def)
	case PatternClose:
		err = ae.execClose(def)
	case PatternClearState:
		err = ae.execClearState(def)
	case PatternUDPSendProto:
		err = ae.execUDPSendProto(def)
	case PatternSetState:
		err = ae.execSetState(def)
	default:
		return fmt.Errorf("未知的动作模式: %s", def.Pattern)
	}

	return err
}

// computeRespKey 根据 Route 计算响应路由键。
func (ae *ActionExecutor) computeRespKey(def *ActionDef) string {
	return ae.adp.ExpectedResponseKey(def.Route)
}

// buildBody 构建消息体字节（序列化 proto 消息）。
func (ae *ActionExecutor) buildBody(def *ActionDef) ([]byte, error) {
	if def.C2SProto == "" {
		return nil, nil
	}
	msg, err := ae.factory.Create(def.C2SProto)
	if err != nil {
		return nil, fmt.Errorf("创建 C2S 消息 %s 失败: %w", def.C2SProto, err)
	}
	if err := ae.bindFields(msg, def.Bindings); err != nil {
		return nil, fmt.Errorf("绑定 C2S 字段失败: %w", err)
	}
	body, err := ae.factory.Serialize(msg)
	if err != nil {
		return nil, fmt.Errorf("序列化 C2S 消息失败: %w", err)
	}
	return body, nil
}

// bindFields 将字段绑定列表应用到 proto 消息。
func (ae *ActionExecutor) bindFields(msg proto.Message, bindings []FieldBind) error {
	for i := range bindings {
		fb := &bindings[i]
		value := ae.resolveFieldValue(fb)

		if value == nil {
			if fb.Required {
				return fmt.Errorf("必需字段 %q 为空 (type=%s, source=%s)", fb.Field, fb.Type, fb.Source)
			}
			if !fb.Optional && isImplicitRequired(fb.Type) {
				stresslog.Warn("[ACTION] 跳过动作: 必需字段为空",
					zap.String("field", fb.Field), zap.String("type", fb.Type),
					zap.String("source", fb.Source))
				return ErrActionSkip
			}
		}

		if fb.StoreAs != "" && value != nil {
			ae.store.Set(fb.StoreAs, value)
		}

		if value == nil || fb.Field == "" {
			continue
		}

		if err := ae.factory.SetField(msg, fb.Field, value); err != nil {
			return fmt.Errorf("字段 %s 设置失败: %w", fb.Field, err)
		}
	}
	return nil
}

// resolveFieldValue 解析字段绑定值，返回 Go 原生类型。
func (ae *ActionExecutor) resolveFieldValue(fb *FieldBind) any {
	var val any

	switch fb.Type {
	case "fixed", "":
		val = fb.Value

	case "state":
		if fb.Source == "" {
			return nil
		}
		val = ae.store.Get(fb.Source)

	case "stateFirst":
		list := ae.store.GetList(fb.Source)
		if len(list) == 0 {
			return nil
		}
		val = list[0]

	case "stateRandom":
		list := ae.store.GetList(fb.Source)
		if len(list) == 0 {
			return nil
		}
		filtered := ae.applyFilters(list, fb.Filters)
		if len(filtered) == 0 {
			return nil
		}
		val = filtered[rand.Intn(len(filtered))]

	case "stateRandomN":
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

	case "stateMapKey":
		m := ae.store.GetMap(fb.Source)
		if len(m) == 0 {
			return nil
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		return keys[rand.Intn(len(keys))]

	case "stateMapValue":
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

	case "randomPick":
		if len(fb.Values) == 0 {
			return nil
		}
		val = fb.Values[rand.Intn(len(fb.Values))]

	case "randomPickN":
		if len(fb.Values) == 0 {
			return nil
		}
		n := fb.Count
		if n <= 0 {
			n = 1
		}
		return pickN(fb.Values, n)

	case "randomPickMap":
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

	case "randomExclude":
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

	case "randomInt":
		lo, hi := fb.Min, fb.Max
		if lo >= hi {
			return lo
		}
		return rand.Intn(hi-lo+1) + lo

	case "randomBool":
		return rand.Intn(2) == 1

	case "randomString":
		length := fb.Length
		if length <= 0 {
			length = 8
		}
		charset := fb.Charset
		if charset == "" {
			charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		}
		return randomStringCharset(length, charset)

	case "listSize":
		return len(ae.store.GetList(fb.Source))

	case "nested":
		if fb.Message == "" {
			return nil
		}
		subMsg, err := ae.factory.Create(fb.Message)
		if err != nil {
			stresslog.Error("[ACTION] nested", zap.String("message", fb.Message), zap.Error(err))
			return nil
		}
		if err := ae.bindFields(subMsg, fb.Bindings); err != nil {
			stresslog.Error("[ACTION] nested bind", zap.String("message", fb.Message), zap.Error(err))
			return nil
		}
		if fb.Wrap {
			return []any{subMsg}
		}
		return subMsg

	case "nestedList":
		if len(fb.Items) == 0 {
			return nil
		}
		result := make([]any, 0, len(fb.Items))
		for _, item := range fb.Items {
			if item.Message == "" {
				continue
			}
			subMsg, err := ae.factory.Create(item.Message)
			if err != nil {
				stresslog.Error("[ACTION] nestedList", zap.String("message", item.Message), zap.Error(err))
				continue
			}
			if err := ae.bindFields(subMsg, item.Bindings); err != nil {
				stresslog.Error("[ACTION] nestedList bind", zap.String("message", item.Message), zap.Error(err))
				continue
			}
			result = append(result, subMsg)
		}
		return result

	default:
		stresslog.Warn("[ACTION] 未知 binding type，按 fixed 处理",
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
// 支持用 | 分隔多条候选路径，按顺序尝试，返回第一个非 nil 的值。
func navigatePath(v any, path string) any {
	if strings.Contains(path, "|") {
		for _, alt := range strings.Split(path, "|") {
			result := navigateSinglePath(v, alt)
			if result != nil {
				return result
			}
		}
		return nil
	}
	return navigateSinglePath(v, path)
}

// navigateSinglePath 从嵌套结构中按单段路径提取值。
func navigateSinglePath(v any, path string) any {
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
	if strings.HasPrefix(addr, "state:") {
		key := addr[len("state:"):]
		return ae.store.GetString(key)
	}
	return addr
}

// execTCPSend TCP 发送（不等响应）
func (ae *ActionExecutor) execTCPSend(def *ActionDef) error {
	body, err := ae.buildBody(def)
	if err != nil {
		if errors.Is(err, ErrActionSkip) {
			return nil
		}
		return err
	}

	secretKey := ae.netSender.GetTCPSecretKey(def.Service)
	packet := ae.adp.EncodeTCP(def.Route, body, secretKey)
	if packet == nil {
		return fmt.Errorf("adapter.EncodeTCP 返回 nil，检查 codec.lua")
	}

	routeKey := ae.adp.ExpectedResponseKey(def.Route)
	ok, n := ae.netSender.TCPSend(def.Service, packet)
	if !ok {
		if def.Optional {
			stresslog.Debug("[ACTION] 可选 TCP 发送失败（已忽略）", zap.String("service", def.Service), zap.String("route", routeKey))
			return nil
		}
		return fmt.Errorf("TCP 发送失败: service=%s route=%s", def.Service, routeKey)
	}

	stresslog.Debug("[ACTION] TCPSend",
		zap.String("service", def.Service), zap.String("route", routeKey),
		zap.String("c2sProto", def.C2SProto), zap.Int("bodyLen", len(body)), zap.Int("pktLen", n))
	return nil
}

// execTCPRequest TCP 请求-响应
func (ae *ActionExecutor) execTCPRequest(def *ActionDef) error {
	body, err := ae.buildBody(def)
	if err != nil {
		if errors.Is(err, ErrActionSkip) {
			return nil
		}
		return err
	}

	routeKey := ae.adp.ExpectedResponseKey(def.Route)
	stresslog.Debug("[ACTION] TCPRequest 发送",
		zap.String("service", def.Service), zap.String("route", routeKey),
		zap.String("c2sProto", def.C2SProto), zap.String("s2cProto", def.S2CProto),
		zap.Int("bodyLen", len(body)))

	secretKey := ae.netSender.GetTCPSecretKey(def.Service)
	packet := ae.adp.EncodeTCP(def.Route, body, secretKey)
	if packet == nil {
		return fmt.Errorf("adapter.EncodeTCP 返回 nil，检查 codec.lua")
	}

	start := time.Now()
	respKey := ae.computeRespKey(def)
	respBody, ok := ae.netSender.TCPRequest(def.Service, packet, respKey)
	elapsed := time.Since(start)
	if !ok {
		if def.Optional {
			stresslog.Debug("[ACTION] 可选 TCP 请求失败（已忽略）",
				zap.String("service", def.Service), zap.String("respKey", respKey),
				zap.Duration("elapsed", elapsed))
			return nil
		}
		return fmt.Errorf("TCP 请求失败: service=%s route=%s respKey=%s elapsed=%v",
			def.Service, routeKey, respKey, elapsed)
	}

	if err := ae.parseAndStoreResponse(def, respBody); err != nil {
		return err
	}

	stresslog.Debug("[ACTION] TCPRequest 成功",
		zap.String("service", def.Service), zap.String("route", routeKey),
		zap.String("respKey", respKey), zap.String("s2cProto", def.S2CProto),
		zap.Int("respBodyLen", len(respBody)), zap.Duration("elapsed", elapsed))
	return nil
}

// execConnect 建立 TCP 连接
func (ae *ActionExecutor) execConnect(def *ActionDef) error {
	addr := ae.resolveAddress(def.Address)
	if addr == "" {
		return fmt.Errorf("TCP 连接地址为空: service=%s", def.Service)
	}
	ok := ae.netSender.ConnectTCP(def.Service, addr)
	if !ok {
		return fmt.Errorf("TCP 连接建立失败: service=%s address=%s", def.Service, addr)
	}
	stresslog.Debug("[ACTION] ConnectTCP 成功", zap.String("service", def.Service), zap.String("address", addr))
	return nil
}

// execConnectUDP 建立 UDP 连接
func (ae *ActionExecutor) execConnectUDP(def *ActionDef) error {
	addr := ae.resolveAddress(def.Address)
	if addr == "" {
		return fmt.Errorf("UDP 连接地址为空: service=%s", def.Service)
	}
	ok := ae.netSender.ConnectUDP(def.Service, addr)
	if !ok {
		return fmt.Errorf("UDP 连接建立失败: service=%s address=%s", def.Service, addr)
	}
	stresslog.Debug("[ACTION] ConnectUDP 成功", zap.String("service", def.Service), zap.String("address", addr))
	return nil
}

// execExchangeKey 发送空包获取密钥并设置到连接
func (ae *ActionExecutor) execExchangeKey(def *ActionDef) error {
	routeKey := ae.adp.ExpectedResponseKey(def.Route)
	stresslog.Debug("[ACTION] ExchangeKey 发送",
		zap.String("service", def.Service), zap.String("route", routeKey))

	packet := ae.adp.EncodeTCP(def.Route, nil, nil)
	if packet == nil {
		return fmt.Errorf("adapter.EncodeTCP 返回 nil，检查 codec.lua")
	}

	start := time.Now()
	respKey := ae.adp.ExpectedResponseKey(def.Route)
	respBody, ok := ae.netSender.TCPRequest(def.Service, packet, respKey)
	elapsed := time.Since(start)
	if !ok || len(respBody) == 0 {
		return fmt.Errorf("交换密钥失败: service=%s route=%s respKey=%s elapsed=%v",
			def.Service, routeKey, respKey, elapsed)
	}
	ae.netSender.SetTCPSecretKey(def.Service, respBody)
	if def.SecretArg != "" {
		ae.store.Set(def.SecretArg, respBody)
	}
	stresslog.Debug("[ACTION] ExchangeKey 成功",
		zap.String("service", def.Service), zap.String("route", routeKey),
		zap.Int("keyLen", len(respBody)), zap.Duration("elapsed", elapsed))
	return nil
}

// execClose 关闭连接
func (ae *ActionExecutor) execClose(def *ActionDef) error {
	target := def.Target
	if target == "" {
		target = "tcp"
	}
	switch target {
	case "udp":
		ae.netSender.CloseUDP(def.Service)
		stresslog.Debug("[ACTION] CloseUDP 成功", zap.String("service", def.Service))
	case "tcp":
		ae.netSender.CloseTCP(def.Service)
		stresslog.Debug("[ACTION] CloseTCP TCP 成功", zap.String("service", def.Service))
	default:
		return fmt.Errorf("未知的关闭目标: %s", target)
	}
	return nil
}

// execClearState 清除 StateStore 中的多个 key
func (ae *ActionExecutor) execClearState(def *ActionDef) error {
	for _, key := range def.Keys {
		ae.store.Delete(key)
	}
	stresslog.Debug("[ACTION] ClearState 成功", zap.Strings("keys", def.Keys))
	return nil
}

// execUDPSendProto UDP 发送 proto 消息
func (ae *ActionExecutor) execUDPSendProto(def *ActionDef) error {
	var body []byte
	if def.C2SProto != "" {
		msg, err := ae.factory.Create(def.C2SProto)
		if err != nil {
			return fmt.Errorf("创建 UDP C2S 消息 %s 失败: %w", def.C2SProto, err)
		}
		if err := ae.bindFields(msg, def.Bindings); err != nil {
			if errors.Is(err, ErrActionSkip) {
				return nil
			}
			return err
		}
		body, err = ae.factory.Serialize(msg)
		if err != nil {
			return fmt.Errorf("序列化 UDP C2S 失败: %w", err)
		}
	}
	routeKey := ae.adp.ExpectedResponseKey(def.Route)
	udpKey := ae.netSender.GetUDPSecretKey(def.Service)
	packet := ae.adp.EncodeUDP(def.Route, body, udpKey)
	if packet == nil {
		return fmt.Errorf("adapter.EncodeUDP 返回 nil，检查 codec.lua")
	}
	ok, n := ae.netSender.UDPSend(def.Service, packet)
	if !ok {
		return fmt.Errorf("UDP 发送失败: route=%s", routeKey)
	}
	stresslog.Debug("[ACTION] UDPSendProto",
		zap.String("route", routeKey), zap.String("c2sProto", def.C2SProto),
		zap.Int("bodyLen", len(body)), zap.Int("pktLen", n))
	return nil
}

// execSetState 从 bindings 设置 StateStore
func (ae *ActionExecutor) execSetState(def *ActionDef) error {
	for i := range def.Bindings {
		fb := &def.Bindings[i]
		val := ae.resolveFieldValue(fb)
		if val == nil {
			continue
		}
		ae.store.Set(fb.Field, val)
	}
	return nil
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
		return fmt.Errorf("解析 S2C 响应 %s 失败: %w", def.S2CProto, err)
	}

	fieldMap := ae.factory.GetFieldMap(respMsg)
	stresslog.Debug("[ACTION] TCPResponseProto", zap.String("proto", def.S2CProto), zap.Int("bodyLen", len(respBody)))

	ae.storeResponse(def.Store, fieldMap)
	return nil
}

// storeResponse 将响应字段存储到 StateStore
func (ae *ActionExecutor) storeResponse(mappings []StoreMapping, fieldMap map[string]any) {
	stresslog.Debug("[ACTION] storeResponse",
		zap.Int("mappingCount", len(mappings)), zap.Int("fieldCount", len(fieldMap)))

	for _, m := range mappings {
		if m.Field == "" {
			if m.Path != "" {
				var rootVal any = fieldMap
				ae.store.Set(m.Setter, navigatePath(rootVal, m.Path))
			} else {
				ae.store.Set(m.Setter, fieldMap)
			}
		} else if val := lookupFieldMap(fieldMap, m.Field); val != nil {
			if m.Path != "" {
				val = navigatePath(val, m.Path)
			}
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

// lookupFieldMap 从 fieldMap 中查找字段值，先精确匹配再忽略大小写。
func lookupFieldMap(fieldMap map[string]any, field string) any {
	if v, ok := fieldMap[field]; ok {
		return v
	}
	lower := strings.ToLower(field)
	for k, v := range fieldMap {
		if strings.ToLower(k) == lower {
			return v
		}
	}
	return nil
}

// execWaitListen 等待监听消息（轮询模式）
func (ae *ActionExecutor) execWaitListen(def *ActionDef) error {
	timeout := def.Timeout
	if timeout <= 0 {
		timeout = 60
	}
	pollMs := def.PollMs
	if pollMs <= 0 {
		pollMs = 100
	}

	respKey := ae.computeRespKey(def)
	routeKey := ae.adp.ExpectedResponseKey(def.Route)
	stresslog.Debug("[ACTION] WaitListen 开始",
		zap.String("service", def.Service), zap.String("route", routeKey),
		zap.String("respKey", respKey), zap.String("s2cProto", def.S2CProto),
		zap.Int("timeoutSec", timeout))

	ae.netSender.EnsureTCPListener(def.Service, respKey)

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	start := time.Now()
	pollCount := 0

	for time.Now().Before(deadline) {
		if ae.ctx != nil && ae.ctx.Err() != nil {
			return ae.ctx.Err()
		}
		pollCount++
		respBody := ae.netSender.GetTCPListenResp(def.Service, respKey)
		if respBody != nil {
			if err := ae.parseAndStoreResponse(def, respBody); err != nil {
				return err
			}
			elapsed := time.Since(start)
			stresslog.Debug("[ACTION] WaitListen 成功",
				zap.String("service", def.Service), zap.String("route", routeKey),
				zap.String("respKey", respKey), zap.String("s2cProto", def.S2CProto),
				zap.Int("respBodyLen", len(respBody)),
				zap.Int("pollCount", pollCount), zap.Duration("elapsed", elapsed))
			return nil
		}
		time.Sleep(time.Duration(pollMs) * time.Millisecond)
	}

	elapsed := time.Since(start)
	if def.Optional {
		stresslog.Warn("[ACTION] 可选 WaitListen 超时（已忽略）",
			zap.String("service", def.Service), zap.String("respKey", respKey),
			zap.Int("pollCount", pollCount), zap.Duration("elapsed", elapsed))
		return nil
	}
	return fmt.Errorf("WaitListen 超时: service=%s route=%s respKey=%s timeout=%ds polls=%d elapsed=%v",
		def.Service, routeKey, respKey, timeout, pollCount, elapsed)
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

// randomStringCharset 生成指定字符集的随机字符串
func randomStringCharset(length int, charset string) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}
