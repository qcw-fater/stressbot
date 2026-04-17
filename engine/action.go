package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	stresslog "stressbot/log"
	"strings"
	"time"

	"stressbot/protox"
	"stressbot/state"

	"google.golang.org/protobuf/proto"
)

// ErrActionSkip 动作跳过信号。
// 当必需字段解析为 nil 时（如 stateRandom 从空列表选取）返回此错误，
// 执行器捕获后视为"静默跳过"而非错误，流程继续执行下一个节点。
var ErrActionSkip = errors.New("action skipped: required field is nil")

// ActionExecutor 声明式动作执行器。
// 根据 ActionDef 的 pattern 执行对应操作：构建 C2S proto 消息 → 序列化 → 编码消息头 →
// 发送 → 接收 S2C 响应 → 解析响应 → 存储 字段到 StateStore。
type ActionExecutor struct {
	netSender NetSender       // 网络发送委托
	store     *state.Store    // Robot 状态存储
	factory   *protox.Factory // 动态消息工厂
	protocol  ProtocolEncoder // 消息头编码器
}

// ProtocolEncoder 消息头编码器接口。
// 从 network.Protocol 抽取，避免 engine 直接依赖 network 包。
type ProtocolEncoder interface {
	EncodeHead(cmd, act uint8, bodyLen uint32, flags uint8) []byte
	CmdAct(cmd, act uint8) int
	HeadSize() int
	BuildPacket(cmd, act uint8, body []byte, secretKey []byte) []byte
	// BuildPacketWithOffset 支持自定义加密偏移量（UDP 帧同步需要明文头部以便服务端查表找密钥）。
	BuildPacketWithOffset(cmd, act uint8, body []byte, secretKey []byte, encryptOffset int) []byte
	// UDPEncryptOffset 返回当前协议 UDP 发送时的加密偏移量（由 header.json 配置，默认 11）。
	UDPEncryptOffset() int
}

// NetSender 网络发送委托接口。
// 将 engine 层与 network 层解耦，Robot 实现此接口注入实际网络操作。
type NetSender interface {
	TCPSend(service string, cmd, act uint8, headAndBody []byte) (bool, int)
	TCPRequest(service string, cmd, act uint8, headAndBody []byte) ([]byte, bool)
	TCPRequestFor(service string, sendCmd, sendAct uint8, headAndBody []byte, respCmd, respAct uint8) ([]byte, bool)
	HTTPPost(path string, formData map[string]string) (statusCode int, body []byte, err error)
	UDPSend(data []byte) bool
	UDPSendPacket(cmd, act uint8, body []byte) bool
	ConnectTCP(service, address string) bool
	ConnectUDP(address string) bool
	CloseTCP(service string)
	CloseUDP()
	GetListenResp(service string, cmdAct int) []byte
	GetSecretKey(service string) []byte
	SetSecretKey(service string, key []byte)
	SetUDPSecretKey(key []byte)
	GetUDPSecretKey() []byte
	EnsureListener(service string, cmd, act uint8)
	// RegisterHeartbeat 为指定服务注册心跳。
	// target: "tcp"（默认）或 "udp"。interval 毫秒，builder 每次返回完整报文（nil 跳过）。
	RegisterHeartbeat(target, service string, intervalMs int, builder func() []byte)
}

// NewActionExecutor 创建声明式动作执行器
func NewActionExecutor(store *state.Store, sender NetSender, factory *protox.Factory, protocol ProtocolEncoder) *ActionExecutor {
	return &ActionExecutor{
		netSender: sender,
		store:     store,
		factory:   factory,
		protocol:  protocol,
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
	case "waitListen", "listenWait":
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
	case "sleep":
		err = ae.execSleep(def)
	case "setState":
		err = ae.execSetState(def)
	case "registerHeartbeat":
		err = ae.execRegisterHeartbeat(def)
	default:
		return fmt.Errorf("未知的动作模式: %s", def.Pattern)
	}

	if err == nil && def.Delay > 0 {
		time.Sleep(time.Duration(def.Delay) * time.Millisecond)
	}
	return err
}

// buildPacket 构建完整的网络报文（消息头 + 消息体）。
func (ae *ActionExecutor) buildPacket(def *ActionDef) ([]byte, error) {
	var body []byte

	if def.C2SProto != "" {
		msg, err := ae.factory.Create(def.C2SProto)
		if err != nil {
			return nil, fmt.Errorf("创建 C2S 消息 %s 失败: %w", def.C2SProto, err)
		}

		if err := ae.bindFields(msg, def.Bindings); err != nil {
			return nil, fmt.Errorf("绑定 C2S 字段失败: %w", err)
		}

		body, err = ae.factory.Serialize(msg)
		if err != nil {
			return nil, fmt.Errorf("序列化 C2S 消息失败: %w", err)
		}
	}

	secretKey := ae.netSender.GetSecretKey(def.Service)
	return ae.protocol.BuildPacket(def.Cmd, def.Act, body, secretKey), nil
}

// bindFields 将字段绑定列表应用到 proto 消息。
func (ae *ActionExecutor) bindFields(msg proto.Message, bindings []FieldBind) error {
	for i := range bindings {
		fb := &bindings[i]
		value := ae.resolveFieldValue(fb)

		if value == nil && fb.isRequired() {
			return ErrActionSkip
		}

		if value == nil {
			// 非必需字段为 nil 时跳过赋值
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

	case "stateRef":
		if fb.Source == "" {
			return nil
		}
		val = ae.store.Get(fb.Source)

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
		// 若有 Path，对每个元素取嵌套字段
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
		// 收集所有 value，按配置过滤后随机选一个
		// 对齐旧 Robot 工具 utils.RandSilenceFilterOne(utils.MapValues(map), predicate)
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
		return fb.Values[rand.Intn(len(fb.Values))]

	case "randomPickN":
		if len(fb.Values) == 0 {
			return nil
		}
		n := fb.Count
		if n <= 0 {
			n = 1
		}
		return pickN(fb.Values, n)

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
		return filtered[rand.Intn(len(filtered))]

	case "randomInt":
		min, max := fb.Min, fb.Max
		if min >= max {
			return min
		}
		return rand.Intn(max-min+1) + min

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

	default:
		return fb.Value
	}

	// 如果配置了 Path，按点分路径深入嵌套 map
	if fb.Path != "" && val != nil {
		val = navigatePath(val, fb.Path)
	}

	return val
}

// navigatePath 按点分路径从嵌套 map/list 中提取值。
// 支持 "[idx]" 数字索引访问 list。
func navigatePath(v any, path string) any {
	current := v
	for _, key := range splitPath(path) {
		if current == nil {
			return nil
		}
		switch c := current.(type) {
		case map[string]any:
			current = c[key]
		case []any:
			// 索引方式 "0"、"1"
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

// splitPath 将点分路径拆分为键列表
func splitPath(path string) []string {
	if path == "" {
		return nil
	}
	result := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == '.' {
			if i > start {
				result = append(result, path[start:i])
			}
			start = i + 1
		}
	}
	if start < len(path) {
		result = append(result, path[start:])
	}
	return result
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
	packet, err := ae.buildPacket(def)
	if err != nil {
		if errors.Is(err, ErrActionSkip) {
			stresslog.DebugF("[ACTION] 跳过动作 %s: 必需字段为空", def.C2SProto)
			return nil
		}
		return err
	}

	ok, n := ae.netSender.TCPSend(def.Service, def.Cmd, def.Act, packet)
	if !ok {
		if def.Optional {
			stresslog.DebugF("[ACTION] 可选 TCP 发送失败（已忽略）: service=%s cmd=%d act=%d", def.Service, def.Cmd, def.Act)
			return nil
		}
		return fmt.Errorf("TCP 发送失败: service=%s cmd=%d act=%d", def.Service, def.Cmd, def.Act)
	}

	stresslog.InfoF("[ACTION] TCPSend 成功: service=%s cmd=%d act=%d bytes=%d", def.Service, def.Cmd, def.Act, n)
	return nil
}

// execTCPRequest TCP 请求-响应
func (ae *ActionExecutor) execTCPRequest(def *ActionDef) error {
	packet, err := ae.buildPacket(def)
	if err != nil {
		if errors.Is(err, ErrActionSkip) {
			stresslog.DebugF("[ACTION] 跳过动作 %s: 必需字段为空", def.C2SProto)
			return nil
		}
		return err
	}

	var respBody []byte
	var ok bool
	if def.RespCmd != 0 || def.RespAct != 0 {
		respBody, ok = ae.netSender.TCPRequestFor(def.Service, def.Cmd, def.Act, packet, def.RespCmd, def.RespAct)
	} else {
		respBody, ok = ae.netSender.TCPRequest(def.Service, def.Cmd, def.Act, packet)
	}
	if !ok {
		if def.Optional {
			stresslog.DebugF("[ACTION] 可选 TCP 请求失败（已忽略）: service=%s cmd=%d act=%d", def.Service, def.Cmd, def.Act)
			return nil
		}
		return fmt.Errorf("TCP 请求失败: service=%s cmd=%d act=%d", def.Service, def.Cmd, def.Act)
	}

	if err := ae.parseAndStoreResponse(def, respBody); err != nil {
		return err
	}

	stresslog.InfoF("[ACTION] TCPRequest 成功: service=%s cmd=%d act=%d", def.Service, def.Cmd, def.Act)
	return nil
}

// execHTTPPost HTTP POST 请求
func (ae *ActionExecutor) execHTTPPost(def *ActionDef) error {
	formData := make(map[string]string)
	for _, fb := range def.FormFields {
		val := ae.resolveFieldValue(&fb)
		if val == nil && fb.isRequired() {
			stresslog.DebugF("[ACTION] 跳过 HTTPPost %s: 必需字段 %s 为空", def.Path, fb.Field)
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
			stresslog.DebugF("[ACTION] 可选 HTTPPost 失败（已忽略）: %v", err)
			return nil
		}
		return fmt.Errorf("HTTP POST 失败 path=%s: %w", def.Path, err)
	}

	if statusCode >= 400 {
		if def.Optional {
			stresslog.DebugF("[ACTION] 可选 HTTPPost 状态码 %d（已忽略）", statusCode)
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

	stresslog.InfoF("[ACTION] HTTPPost 成功: path=%s status=%d", def.Path, statusCode)
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
	stresslog.InfoF("[ACTION] ConnectTCP 成功: service=%s address=%s", def.Service, addr)
	return nil
}

// execConnectUDP 建立 UDP 连接
func (ae *ActionExecutor) execConnectUDP(def *ActionDef) error {
	addr := ae.resolveAddress(def.Address)
	if addr == "" {
		return fmt.Errorf("UDP 连接地址为空")
	}
	ok := ae.netSender.ConnectUDP(addr)
	if !ok {
		return fmt.Errorf("UDP 连接建立失败: address=%s", addr)
	}
	stresslog.InfoF("[ACTION] ConnectUDP 成功: address=%s", addr)
	return nil
}

// execExchangeKey 发送 (0,0) 空包获取密钥，并设置到连接
func (ae *ActionExecutor) execExchangeKey(def *ActionDef) error {
	// 构建空包（无加密）
	packet := ae.protocol.BuildPacket(0, 0, nil, nil)
	respBody, ok := ae.netSender.TCPRequest(def.Service, 0, 0, packet)
	if !ok || len(respBody) == 0 {
		return fmt.Errorf("交换密钥失败: service=%s", def.Service)
	}
	ae.netSender.SetSecretKey(def.Service, respBody)
	if def.SecretArg != "" {
		ae.store.Set(def.SecretArg, respBody)
	}
	stresslog.InfoF("[ACTION] ExchangeKey 成功: service=%s keyLen=%d", def.Service, len(respBody))
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
		ae.netSender.CloseUDP()
		stresslog.InfoF("[ACTION] Close UDP 成功")
	case "tcp":
		ae.netSender.CloseTCP(def.Service)
		stresslog.InfoF("[ACTION] Close TCP 成功: service=%s", def.Service)
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
	stresslog.InfoF("[ACTION] ClearState 成功: keys=%v", def.Keys)
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
	if !ae.netSender.UDPSendPacket(def.Cmd, def.Act, body) {
		return fmt.Errorf("UDP 发送失败: cmd=%d act=%d", def.Cmd, def.Act)
	}
	return nil
}

// execUDPSendRaw UDP 发送自定义二进制
func (ae *ActionExecutor) execUDPSendRaw(def *ActionDef) error {
	body, err := ae.buildRawBody(def.RawBody)
	if err != nil {
		return fmt.Errorf("构建 UDPRaw body 失败: %w", err)
	}
	if !ae.netSender.UDPSendPacket(def.Cmd, def.Act, body) {
		return fmt.Errorf("UDP 发送失败: cmd=%d act=%d", def.Cmd, def.Act)
	}
	return nil
}

// execSleep 暂停指定毫秒
func (ae *ActionExecutor) execSleep(def *ActionDef) error {
	d := def.Delay
	if d <= 0 {
		return nil
	}
	time.Sleep(time.Duration(d) * time.Millisecond)
	return nil
}

// execRegisterHeartbeat 注册连接心跳。
// 根据 ActionDef 的 Target/Service/IntervalMs/Cmd/Act/RawBody/C2SProto 构造 builder。
// 如果需要 Lua 自定义 body，请改用 lua 模式+network.register_heartbeat。
func (ae *ActionExecutor) execRegisterHeartbeat(def *ActionDef) error {
	interval := def.IntervalMs
	if interval <= 0 {
		return fmt.Errorf("registerHeartbeat intervalMs 必须 > 0")
	}
	target := def.Target
	if target == "" {
		target = "tcp"
	}

	cmd := def.Cmd
	act := def.Act
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
		// UDP 发送采用 offset 加密：明文前缀供服务端查密钥表，剩余部分加密
		if target == "udp" {
			encOffset := ae.protocol.UDPEncryptOffset()
			udpKey := ae.netSender.GetUDPSecretKey()
			if len(body) <= encOffset || len(udpKey) == 0 {
				return ae.protocol.BuildPacket(cmd, act, body, nil)
			}
			return ae.protocol.BuildPacketWithOffset(cmd, act, body, udpKey, encOffset)
		}
		secretKey := ae.netSender.GetSecretKey(def.Service)
		return ae.protocol.BuildPacket(cmd, act, body, secretKey)
	}

	ae.netSender.RegisterHeartbeat(target, def.Service, interval, builder)
	stresslog.InfoF("[ACTION] RegisterHeartbeat: target=%s service=%s interval=%dms cmd=%d act=%d",
		target, def.Service, interval, cmd, act)
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
		min, max := f.Min, f.Max
		if min >= max {
			val = min
		} else {
			val = rand.Intn(max-min+1) + min
		}
	default:
		val = f.Value
	}

	switch f.Type {
	case "u8", "i8":
		return binary.Write(buf, binary.LittleEndian, uint8(toInt64(val)))
	case "u16", "i16", "random_u16":
		return binary.Write(buf, binary.LittleEndian, uint16(toInt64(val)))
	case "u32", "i32":
		return binary.Write(buf, binary.LittleEndian, uint32(toInt64(val)))
	case "u64", "i64", "time_ms":
		return binary.Write(buf, binary.LittleEndian, uint64(toInt64(val)))
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
	stresslog.DebugF("[ACTION] TCPResponseProto %s: fields=%v bodyLen=%d", def.S2CProto, fieldMap, len(respBody))

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

	cmd := def.Cmd
	act := def.Act
	if def.RespCmd != 0 || def.RespAct != 0 {
		cmd = def.RespCmd
		act = def.RespAct
	}
	cmdAct := ae.protocol.CmdAct(cmd, act)

	ae.netSender.EnsureListener(def.Service, cmd, act)

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)

	for time.Now().Before(deadline) {
		respBody := ae.netSender.GetListenResp(def.Service, cmdAct)
		if respBody != nil {
			if err := ae.parseAndStoreResponse(def, respBody); err != nil {
				return err
			}
			stresslog.InfoF("[ACTION] WaitListen 成功: service=%s cmd=%d act=%d", def.Service, cmd, act)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	if def.Optional {
		stresslog.WarnF("[ACTION] 可选 WaitListen 超时（已忽略）: service=%s cmd=%d act=%d", def.Service, cmd, act)
		return nil
	}
	return fmt.Errorf("WaitListen 超时: service=%s cmd=%d act=%d timeout=%ds",
		def.Service, cmd, act, timeout)
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
// 注意：对于 proto3 消息，未设置的标量字段（如 int32=0、string=""）在 GetFieldMap
// 中返回 nil。为了让 "modeId != 0" 这类过滤条件符合语义，当 a 为 nil 且 b 为数字时，
// 视 a 为数字 0 处理；当 a 为 nil 且 b 为字符串时，视 a 为 ""。
func compareValues(a, b any, op string) bool {
	aNum, aIsNum := toFloat64safe(a)
	bNum, bIsNum := toFloat64safe(b)

	// nil → 对齐 proto3 默认值（scalar 类型），便于 neq/eq 判定
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
		// b 为列表，a 是否在其中
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
		// a 为 map{startTime,endTime} 或类似，b 为一个时间戳或当前时间
		// 此处简化：若 b 非数字则用当前 HH*60+MM 分钟数
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
		// 对齐旧工具 OnHandleCreateNormalTeam 的 DailyOpenTime 过滤：
		// a 为 RuleTimeInfo 列表（map 数组，字段: StartHour/StartMinute/EndHour/EndMinute）。
		// 当 a 为 nil/空列表时，视为"无时间限制"，返回 true（接受该项）。
		// 否则只要当前时间处于任一窗口内即返回 true。
		if a == nil {
			return true
		}
		list, ok := a.([]any)
		if !ok {
			// 容错：单个窗口对象也接受
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
		nowHour, nowMinute := t.Hour(), t.Minute()
		for _, it := range list {
			entry, ok := it.(map[string]any)
			if !ok {
				continue
			}
			sh, _ := toFloat64safe(firstNonNil(entry["StartHour"], entry["startHour"]))
			sm, _ := toFloat64safe(firstNonNil(entry["StartMinute"], entry["startMinute"]))
			eh, _ := toFloat64safe(firstNonNil(entry["EndHour"], entry["endHour"]))
			em, _ := toFloat64safe(firstNonNil(entry["EndMinute"], entry["endMinute"]))
			if nowHour >= int(sh) && nowHour <= int(eh) &&
				(nowMinute >= int(sm) || nowMinute <= int(em)) {
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

// toFloat64safe 尝试将 any 值转换为 float64
// firstNonNil 返回第一个非 nil 的值。
// 用于在 GetFieldMap 返回的 map 中兼容 proto 字段名大小写差异
// （如 StartHour 与 startHour）。
func firstNonNil(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

func toFloat64safe(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	default:
		return 0, false
	}
}

// toInt64 将 any 转为 int64
func toInt64(v any) int64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case uint:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case bool:
		if n {
			return 1
		}
		return 0
	default:
		return 0
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

// pickN 从列表中随机选择 N 个不重复元素（Fisher-Yates）
func pickN(list []any, n int) []any {
	if n >= len(list) {
		// 返回整个列表（可以考虑打乱，但这里保持简单）
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
