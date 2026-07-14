package errcode

// ErrorCode 统一错误码类型。码段契约：< 100 框架码（工具自产，本 registry 分配），≥ 100 业务码（服务器返回）。单一 code 唯一标识，不再需要 Kind 维度。
type ErrorCode uint64

const (
	// 网络层 (1-10)
	ErrConnNotFound   ErrorCode = 1 // 连接未建立（GetTCPConn/GetUDPConn 返回 nil）
	ErrConnClosed     ErrorCode = 2 // 连接已关闭（isClose == 1）
	ErrSendFailed     ErrorCode = 3 // socket 写入失败（Send 返回 false）
	ErrRecvTimeout    ErrorCode = 4 // 等待响应超时（select timeout）
	ErrConnDropped    ErrorCode = 5 // 等待期间连接被对端断开（gnet OnClose 触发的 ctx.Done）
	ErrActionCanceled ErrorCode = 6 // 等待期间连接被本地主动关闭（任务停止 / robot.Stop / 业务 Close）

	// 协议层 (11-20)
	ErrEncodeFailed ErrorCode = 11 // Adapter 编码返回 nil
	ErrParseFailed  ErrorCode = 12 // S2C proto 解析失败

	// 构建层 (21-30)
	ErrCreateMsg  ErrorCode = 21 // 创建 C2S proto 消息失败
	ErrBindField  ErrorCode = 22 // 必需字段绑定失败（Required=true）
	ErrSerialize  ErrorCode = 23 // C2S 消息序列化失败
	ErrExecFailed ErrorCode = 24 // 动作执行失败（onError.strategy=abort）

	// 监听层 (31-40)
	ErrListenTimeout  ErrorCode = 31 // TCP/UDP Listen 轮询超时
	ErrListenRegister ErrorCode = 32 // 注册持久监听失败

	// 配置层 (41-50)
	ErrAddrEmpty       ErrorCode = 41 // 连接地址为空
	ErrURLEmpty        ErrorCode = 42 // HTTP URL 为空
	ErrURLScheme       ErrorCode = 43 // HTTP URL 协议错误（缺 http:// 前缀）
	ErrUnknownPattern  ErrorCode = 44 // 未知动作模式（配置错误）
	ErrHTTPBuild       ErrorCode = 45 // http.NewRequest 失败
	ErrHTTPReadBody    ErrorCode = 46 // 读取 HTTP 响应体失败
	ErrMarshalBody     ErrorCode = 47 // JSON/form 请求体序列化失败
	ErrHTTPStatus      ErrorCode = 48 // HTTP 响应状态码非 2xx
	ErrHeartbeatConfig ErrorCode = 49 // 声明式心跳配置错误（intervalMs<=0 / route 缺失 / 字段非法）
	ErrStateConfig     ErrorCode = 50 // 状态动作配置错误（如 clearState 清除内置状态）

	// Lua 层 (51-60)
	ErrLuaNotInit     ErrorCode = 51 // Lua 运行时未初始化
	ErrLuaNoScript    ErrorCode = 52 // lua 动作缺少 script 配置
	ErrLuaExecFailed  ErrorCode = 53 // Lua 脚本执行异常
	ErrLuaScriptCheck ErrorCode = 54 // 脚本校验失败

	// 回调层 (61-70)
	ErrCallbackLua   ErrorCode = 61 // Lua 回调脚本执行失败
	ErrCallbackParse ErrorCode = 62 // 推送消息解析失败
)

// CodeInfo 单条错误码元数据，HTTP 端点 /sbot/api/error-codes 返回此结构。
type CodeInfo struct {
	Code uint64 `json:"code"` // 数值错误码
	Name string `json:"name"` // 大写下划线格式的错误码名称（如 "CONN_NOT_FOUND"）
}

// codeRegistry 是唯一真理源：新增/重命名错误码只动这里。
// String() 和 AllCodes() 都从它派生，避免两份独立 switch/数组漂移导致的不一致 bug。
var codeRegistry = []CodeInfo{
	{uint64(ErrConnNotFound), "CONN_NOT_FOUND"},
	{uint64(ErrConnClosed), "CONN_CLOSED"},
	{uint64(ErrSendFailed), "SEND_FAILED"},
	{uint64(ErrRecvTimeout), "RECV_TIMEOUT"},
	{uint64(ErrConnDropped), "CONN_DROPPED"},
	{uint64(ErrActionCanceled), "ACTION_CANCELED"},
	{uint64(ErrEncodeFailed), "ENCODE_FAILED"},
	{uint64(ErrParseFailed), "PARSE_FAILED"},
	{uint64(ErrCreateMsg), "CREATE_MSG"},
	{uint64(ErrBindField), "BIND_FIELD"},
	{uint64(ErrSerialize), "SERIALIZE"},
	{uint64(ErrExecFailed), "EXEC_FAILED"},
	{uint64(ErrListenTimeout), "LISTEN_TIMEOUT"},
	{uint64(ErrListenRegister), "LISTEN_REGISTER"},
	{uint64(ErrAddrEmpty), "ADDR_EMPTY"},
	{uint64(ErrURLEmpty), "URL_EMPTY"},
	{uint64(ErrURLScheme), "URL_SCHEME"},
	{uint64(ErrUnknownPattern), "UNKNOWN_PATTERN"},
	{uint64(ErrHTTPBuild), "HTTP_BUILD"},
	{uint64(ErrHTTPReadBody), "HTTP_READ_BODY"},
	{uint64(ErrMarshalBody), "MARSHAL_BODY"},
	{uint64(ErrHTTPStatus), "HTTP_STATUS"},
	{uint64(ErrHeartbeatConfig), "HEARTBEAT_CONFIG"},
	{uint64(ErrStateConfig), "STATE_CONFIG"},
	{uint64(ErrLuaNotInit), "LUA_NOT_INIT"},
	{uint64(ErrLuaNoScript), "LUA_NO_SCRIPT"},
	{uint64(ErrLuaExecFailed), "LUA_EXEC_FAILED"},
	{uint64(ErrLuaScriptCheck), "LUA_SCRIPT_CHECK"},
	{uint64(ErrCallbackLua), "CALLBACK_LUA"},
	{uint64(ErrCallbackParse), "CALLBACK_PARSE"},
}

// 派生：包初始化时一次性建索引，String() O(1) 查询。
var codeNameIndex = func() map[uint64]string {
	m := make(map[uint64]string, len(codeRegistry))
	for _, c := range codeRegistry {
		m[c.Code] = c.Name
	}
	return m
}()

// String 自描述错误码（用于日志/CSV/前端 i18n 兜底）。
// 未注册的 code（含服务端 headerErr）返回空字符串。
func (c ErrorCode) String() string {
	if name, ok := codeNameIndex[uint64(c)]; ok {
		return name
	}
	return ""
}

// AllCodes 返回全部框架错误码定义，供 GET /sbot/api/error-codes 透传给前端。
// 返回切片副本，调用方修改不影响内部状态。
func AllCodes() []CodeInfo {
	out := make([]CodeInfo, len(codeRegistry))
	copy(out, codeRegistry)
	return out
}
