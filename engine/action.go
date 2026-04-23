package engine

import (
	"bytes"
	"encoding/binary"
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

// ErrActionSkip 动作跳过信号。
var ErrActionSkip = errors.New("action skipped: required field is nil")

// ActionExecutor 声明式动作执行器。
type ActionExecutor struct {
	netSender NetSender       // 网络发送委托
	store     *state.Store    // Robot 状态存储
	factory   *protox.Factory // 动态消息工厂
	adp       adapter.Adapter // 协议适配器
}

// NetSender 网络发送委托接口。
type NetSender interface {
	TCPSend(service string, packet []byte) (bool, int)
	TCPRequest(service string, packet []byte, responseKey string) ([]byte, bool)
	HTTPPost(path string, formData map[string]string) (statusCode int, body []byte, err error)
	UDPSend(service string, data []byte) bool
	ConnectTCP(service, address string) bool
	ConnectUDP(service, address string) bool
	CloseTCP(service string)
	CloseUDP(service string)
	GetListenResp(service string, responseKey string) []byte
	GetSecretKey(service string) []byte
	SetSecretKey(service string, key []byte)
	SetUDPSecretKey(service string, key []byte)
	GetUDPSecretKey(service string) []byte
	EnsureListener(service string, responseKey string)
	RegisterHeartbeat(target, service string, intervalMs int, builder func() []byte)
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

// Execute 执行声明式动作
func (ae *ActionExecutor) Execute(def *ActionDef) error {
	var err error
	switch def.Pattern {
	case "tcpSend":
		err = ae.execTCPSend(def)
	case "tcpRequest":
		err = ae.execTCPRequest(def)
	case "httpPost":
		err = ae.execHTTPPost(def)
	case "connect":
		err = ae.execConnect(def)
	case "connectUDP":
		err = ae.execConnectUDP(def)
	case "waitListen":
		err = ae.execWaitListen(def)
	case "exchangeKey":
		err = ae.execExchangeKey(def)
	case "close":
		err = ae.execClose(def)
	case "clearState":
		err = ae.execClearState(def)
	case "udpSendProto":
		err = ae.execUDPSendProto(def)
	case "udpSendRaw":
		err = ae.execUDPSendRaw(def)
	case "setState":
		err = ae.execSetState(def)
	case "registerHeartbeat":
		err = ae.execRegisterHeartbeat(def)
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

		if value == nil && fb.isRequired() {
			return ErrActionSkip
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
			if !deepEqual(it, exclude) {
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
			return fb.Value
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
			var idx int
			_, err := fmt.Sscanf(key, "%d", &idx)
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
			stresslog.Debug("[ACTION] 跳过动作: 必需字段为空", zap.String("proto", def.C2SProto))
			return nil
		}
		return err
	}

	secretKey := ae.netSender.GetSecretKey(def.Service)
	packet := ae.adp.Encode(def.Route, body, secretKey)
	if packet == nil {
		return fmt.Errorf("adapter.Encode 返回 nil，检查 codec.lua")
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
			stresslog.Debug("[ACTION] 跳过动作: 必需字段为空", zap.String("proto", def.C2SProto))
			return nil
		}
		return err
	}

	routeKey := ae.adp.ExpectedResponseKey(def.Route)
	stresslog.Debug("[ACTION] TCPRequest 发送",
		zap.String("service", def.Service), zap.String("route", routeKey),
		zap.String("c2sProto", def.C2SProto), zap.String("s2cProto", def.S2CProto),
		zap.Int("bodyLen", len(body)))

	secretKey := ae.netSender.GetSecretKey(def.Service)
	packet := ae.adp.Encode(def.Route, body, secretKey)
	if packet == nil {
		return fmt.Errorf("adapter.Encode 返回 nil，检查 codec.lua")
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

// execHTTPPost HTTP POST 请求
func (ae *ActionExecutor) execHTTPPost(def *ActionDef) error {
	formData := make(map[string]string)
	for _, fb := range def.FormFields {
		val := ae.resolveFieldValue(&fb)
		if val == nil && fb.isRequired() {
			stresslog.Debug("[ACTION] 跳过 HTTPPost: 必需字段为空", zap.String("path", def.Path), zap.String("field", fb.Field))
			return nil
		}
		if val == nil {
			continue
		}
		formData[fb.Field] = fmt.Sprintf("%v", val)
	}

	statusCode, body, err := ae.netSender.HTTPPost(def.Path, formData)
	if err != nil {
		if def.Optional {
			stresslog.Debug("[ACTION] 可选 HTTPPost 失败（已忽略）", zap.Error(err))
			return nil
		}
		return fmt.Errorf("HTTP POST 失败 path=%s: %w", def.Path, err)
	}

	if statusCode >= 400 {
		if def.Optional {
			stresslog.Debug("[ACTION] 可选 HTTPPost 状态码（已忽略）", zap.Int("statusCode", statusCode))
			return nil
		}
		return fmt.Errorf("HTTP POST 返回错误状态码: %d path=%s", statusCode, def.Path)
	}

	if def.S2CProto != "" && len(body) > 0 && len(def.Store) > 0 {
		respMsg, err := ae.factory.Parse(def.S2CProto, body)
		if err != nil {
			return fmt.Errorf("解析 HTTP 响应 %s 失败: %w", def.S2CProto, err)
		}
		fieldMap := ae.factory.GetFieldMap(respMsg)
		ae.storeResponse(def.Store, fieldMap)
	}

	stresslog.Debug("[ACTION] HTTPPost 成功", zap.String("path", def.Path), zap.Int("status", statusCode))
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

	packet := ae.adp.Encode(def.Route, nil, nil)
	if packet == nil {
		return fmt.Errorf("adapter.Encode 返回 nil，检查 codec.lua")
	}

	start := time.Now()
	respKey := ae.adp.ExpectedResponseKey(def.Route)
	respBody, ok := ae.netSender.TCPRequest(def.Service, packet, respKey)
	elapsed := time.Since(start)
	if !ok || len(respBody) == 0 {
		return fmt.Errorf("交换密钥失败: service=%s route=%s respKey=%s elapsed=%v",
			def.Service, routeKey, respKey, elapsed)
	}
	ae.netSender.SetSecretKey(def.Service, respBody)
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
		stresslog.Debug("[ACTION] Close UDP 成功")
	case "tcp":
		ae.netSender.CloseTCP(def.Service)
		stresslog.Debug("[ACTION] Close TCP 成功", zap.String("service", def.Service))
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
	if !ae.netSender.UDPSend(def.Service, packet) {
		return fmt.Errorf("UDP 发送失败: route=%s", routeKey)
	}
	stresslog.Debug("[ACTION] UDPSendProto",
		zap.String("route", routeKey), zap.String("c2sProto", def.C2SProto),
		zap.Int("bodyLen", len(body)), zap.Int("pktLen", len(packet)))
	return nil
}

// execUDPSendRaw UDP 发送自定义二进制
func (ae *ActionExecutor) execUDPSendRaw(def *ActionDef) error {
	body, err := ae.buildRawBody(def.RawBody)
	if err != nil {
		return fmt.Errorf("构建 UDPRaw body 失败: %w", err)
	}
	routeKey := ae.adp.ExpectedResponseKey(def.Route)
	udpKey := ae.netSender.GetUDPSecretKey(def.Service)
	packet := ae.adp.EncodeUDP(def.Route, body, udpKey)
	if packet == nil {
		return fmt.Errorf("adapter.EncodeUDP 返回 nil，检查 codec.lua")
	}
	if !ae.netSender.UDPSend(def.Service, packet) {
		return fmt.Errorf("UDP 发送失败: route=%s", routeKey)
	}
	stresslog.Debug("[ACTION] UDPSendRaw",
		zap.String("route", routeKey),
		zap.Int("bodyLen", len(body)), zap.Int("pktLen", len(packet)))
	return nil
}

// execRegisterHeartbeat 注册连接心跳。
func (ae *ActionExecutor) execRegisterHeartbeat(def *ActionDef) error {
	interval := def.IntervalMs
	if interval <= 0 {
		return fmt.Errorf("registerHeartbeat intervalMs 必须 > 0")
	}
	target := def.Target
	if target == "" {
		target = "tcp"
	}

	route := def.Route
	rawBody := def.RawBody
	c2sProto := def.C2SProto
	bindings := def.Bindings

	builder := func() []byte {
		var body []byte
		var err error
		if len(rawBody) > 0 {
			body, err = ae.buildRawBody(rawBody)
			if err != nil {
				return nil
			}
		} else if c2sProto != "" {
			msg, err := ae.factory.Create(c2sProto)
			if err != nil {
				return nil
			}
			if err := ae.bindFields(msg, bindings); err != nil {
				return nil
			}
			body, err = ae.factory.Serialize(msg)
			if err != nil {
				return nil
			}
		}
		if target == "udp" {
			udpKey := ae.netSender.GetUDPSecretKey(def.Service)
			return ae.adp.EncodeUDP(route, body, udpKey)
		}
		secretKey := ae.netSender.GetSecretKey(def.Service)
		return ae.adp.Encode(route, body, secretKey)
	}

	ae.netSender.RegisterHeartbeat(target, def.Service, interval, builder)
	stresslog.Debug("[ACTION] RegisterHeartbeat",
		zap.String("target", target), zap.String("service", def.Service), zap.Int("intervalMs", interval))
	return nil
}

// execSetState 从 bindings 设置 StateStore
func (ae *ActionExecutor) execSetState(def *ActionDef) error {
	for i := range def.Bindings {
		fb := &def.Bindings[i]
		val := ae.resolveFieldValue(fb)
		if val == nil && fb.isRequired() {
			continue
		}
		ae.store.Set(fb.Field, val)
	}
	return nil
}

// buildRawBody 构建二进制 body
func (ae *ActionExecutor) buildRawBody(fields []RawField) ([]byte, error) {
	buf := new(bytes.Buffer)
	for _, f := range fields {
		if err := ae.writeRawField(buf, &f); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func (ae *ActionExecutor) writeRawField(buf *bytes.Buffer, f *RawField) error {
	var val any
	switch {
	case f.Counter != "":
		val = ae.store.IncrementInt64(f.Counter)
	case f.Source != "":
		val = ae.store.Get(f.Source)
	case f.Type == "time_ms":
		val = time.Now().UnixMilli()
	case f.Type == "random_u16":
		lo, hi := f.Min, f.Max
		if lo >= hi {
			val = lo
		} else {
			val = rand.Intn(hi-lo+1) + lo
		}
	default:
		val = f.Value
	}

	switch f.Type {
	case "u8", "i8":
		return binary.Write(buf, binary.LittleEndian, uint8(state.ToInt64(val)))
	case "u16", "i16", "random_u16":
		return binary.Write(buf, binary.LittleEndian, uint16(state.ToInt64(val)))
	case "u32", "i32":
		return binary.Write(buf, binary.LittleEndian, uint32(state.ToInt64(val)))
	case "u64", "i64", "time_ms":
		return binary.Write(buf, binary.LittleEndian, uint64(state.ToInt64(val)))
	case "bytes":
		b, _ := val.([]byte)
		if b == nil {
			if s, ok := val.(string); ok {
				b = []byte(s)
			}
		}
		if f.Length > 0 && len(b) < f.Length {
			pad := make([]byte, f.Length-len(b))
			b = append(b, pad...)
		} else if f.Length > 0 && len(b) > f.Length {
			b = b[:f.Length]
		}
		_, err := buf.Write(b)
		return err
	default:
		return fmt.Errorf("未知的 RawField 类型: %s", f.Type)
	}
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
	for _, m := range mappings {
		if m.Field == "" {
			if m.Path != "" {
				var rootVal any = fieldMap
				ae.store.Set(m.Setter, navigatePath(rootVal, m.Path))
			} else {
				ae.store.Set(m.Setter, fieldMap)
			}
		} else if val, ok := fieldMap[m.Field]; ok {
			if m.Path != "" && val != nil {
				val = navigatePath(val, m.Path)
			}
			ae.store.Set(m.Setter, val)
		}
	}
}

// execWaitListen 等待监听消息（轮询模式）
func (ae *ActionExecutor) execWaitListen(def *ActionDef) error {
	timeout := def.Timeout
	if timeout <= 0 {
		timeout = 60
	}

	respKey := ae.computeRespKey(def)
	routeKey := ae.adp.ExpectedResponseKey(def.Route)
	stresslog.Debug("[ACTION] WaitListen 开始",
		zap.String("service", def.Service), zap.String("route", routeKey),
		zap.String("respKey", respKey), zap.String("s2cProto", def.S2CProto),
		zap.Int("timeoutSec", timeout))

	ae.netSender.EnsureListener(def.Service, respKey)

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	start := time.Now()
	pollCount := 0

	for time.Now().Before(deadline) {
		pollCount++
		respBody := ae.netSender.GetListenResp(def.Service, respKey)
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
		time.Sleep(100 * time.Millisecond)
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

		if !compareValues(fieldVal, targetVal, f.Op) {
			return false
		}
	}
	return true
}

// compareValues 按操作符比较两个值。
func compareValues(a, b any, op string) bool {
	aNum, aIsNum := toFloat64safe(a)
	bNum, bIsNum := toFloat64safe(b)

	if a == nil && bIsNum {
		aNum, aIsNum = 0, true
	}
	if b == nil && aIsNum {
		bNum, bIsNum = 0, true
	}

	switch op {
	case "eq", "==", "":
		if aIsNum && bIsNum {
			return aNum == bNum
		}
		if a == nil && b == nil {
			return true
		}
		if a == nil || b == nil {
			return false
		}
		return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)

	case "neq", "!=":
		if aIsNum && bIsNum {
			return aNum != bNum
		}
		if a == nil && b == nil {
			return false
		}
		if a == nil || b == nil {
			return true
		}
		return fmt.Sprintf("%v", a) != fmt.Sprintf("%v", b)

	case "gt", ">":
		if aIsNum && bIsNum {
			return aNum > bNum
		}
		return false

	case "gte", ">=":
		if aIsNum && bIsNum {
			return aNum >= bNum
		}
		return false

	case "lt", "<":
		if aIsNum && bIsNum {
			return aNum < bNum
		}
		return false

	case "lte", "<=":
		if aIsNum && bIsNum {
			return aNum <= bNum
		}
		return false

	case "contains":
		aStr := fmt.Sprintf("%v", a)
		bStr := fmt.Sprintf("%v", b)
		return strings.Contains(aStr, bStr)

	case "in":
		list, ok := b.([]any)
		if !ok {
			return false
		}
		for _, it := range list {
			if deepEqual(a, it) {
				return true
			}
		}
		return false

	case "timeWindow":
		var now int
		if bIsNum {
			now = int(bNum)
		} else {
			t := time.Now()
			now = t.Hour()*60 + t.Minute()
		}
		m, ok := a.(map[string]any)
		if !ok {
			return true
		}
		start, _ := toFloat64safe(m["startTime"])
		end, _ := toFloat64safe(m["endTime"])
		return float64(now) >= start && float64(now) <= end

	case "dailyTimeWindow":
		if a == nil {
			return true
		}
		list, ok := a.([]any)
		if !ok {
			if single, isMap := a.(map[string]any); isMap {
				list = []any{single}
			} else {
				return true
			}
		}
		if len(list) == 0 {
			return true
		}
		t := time.Now()
		nowTotalMin := t.Hour()*60 + t.Minute()
		for _, it := range list {
			entry, ok := it.(map[string]any)
			if !ok {
				continue
			}
			sh, _ := toFloat64safe(firstNonNil(entry["StartHour"], entry["startHour"]))
			sm, _ := toFloat64safe(firstNonNil(entry["StartMinute"], entry["startMinute"]))
			eh, _ := toFloat64safe(firstNonNil(entry["EndHour"], entry["endHour"]))
			em, _ := toFloat64safe(firstNonNil(entry["EndMinute"], entry["endMinute"]))
			startMin := int(sh)*60 + int(sm)
			endMin := int(eh)*60 + int(em)
			if nowTotalMin >= startMin && nowTotalMin <= endMin {
				return true
			}
		}
		return false

	case "notNil":
		return a != nil

	case "isNil":
		return a == nil

	default:
		return false
	}
}

// firstNonNil 返回第一个非 nil 的值。
func firstNonNil(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// toFloat64safe 尝试将 any 值转换为 float64
func toFloat64safe(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	r := state.ToFloat64(v)
	switch v.(type) {
	case int, int32, int64, uint, uint32, uint64, float64, float32:
		return r, true
	default:
		return 0, false
	}
}

// deepEqual 简单的深度相等判断
func deepEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	aNum, aOk := toFloat64safe(a)
	bNum, bOk := toFloat64safe(b)
	if aOk && bOk {
		return aNum == bNum
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
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
