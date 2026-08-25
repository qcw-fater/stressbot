// Package errcode 定义统一的错误码体系：单一 ErrorCode 维度携带框架/业务错误，
// 码段契约 < 100 为框架自产码、≥ 100 为服务器返回的业务码，动作失败统一封装为 ActionError。
package errcode

// ErrorCode 统一错误码类型。码段契约：< 100 框架码（工具自产，本 registry 分配），≥ 100 业务码（服务器返回）。单一 code 唯一标识，不再需要 Kind 维度。
type ErrorCode uint64

// 29 个框架错误码按码段分组，每组首个常量上方注明所属层与码段区间；行尾注释为触发条件。
// ≥ 100 的业务码由服务器经 errors.json 描述，不在此登记。
const (
	// ErrConnNotFound 表示目标连接尚未建立（网络层 1-10）。
	ErrConnNotFound   ErrorCode = 1 // 连接未建立（GetTCPConn/GetUDPConn 返回 nil）
	ErrConnClosed     ErrorCode = 2 // 连接已关闭（isClose == 1）
	ErrSendFailed     ErrorCode = 3 // socket 写入失败（Send 返回 false）
	ErrRecvTimeout    ErrorCode = 4 // 等待响应超时（select timeout）
	ErrConnDropped    ErrorCode = 5 // 等待期间连接被对端断开（gnet OnClose 触发的 ctx.Done）
	ErrActionCanceled ErrorCode = 6 // 等待期间连接被本地主动关闭（任务停止 / robot.Stop / 业务 Close）

	// ErrEncodeFailed 表示 Adapter 编码失败（协议层 11-20）。
	ErrEncodeFailed ErrorCode = 11 // Adapter 编码返回 nil
	ErrParseFailed  ErrorCode = 12 // S2C proto 解析失败

	// ErrCreateMsg 表示创建 C2S protobuf 消息失败（构建层 21-30）。
	ErrCreateMsg  ErrorCode = 21 // 创建 C2S proto 消息失败
	ErrBindField  ErrorCode = 22 // 必需字段绑定失败（Required=true）
	ErrSerialize  ErrorCode = 23 // C2S 消息序列化失败
	ErrExecFailed ErrorCode = 24 // 动作执行失败（onError.strategy=abort）

	// ErrListenTimeout 表示等待监听消息超时（监听层 31-40）。
	ErrListenTimeout  ErrorCode = 31 // TCP/UDP Listen 轮询超时
	ErrListenRegister ErrorCode = 32 // 注册持久监听失败

	// ErrAddrEmpty 表示连接地址为空（配置层 41-50）。
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

	// ErrLuaNotInit 表示 Lua 运行时尚未初始化（Lua 层 51-60）。
	ErrLuaNotInit     ErrorCode = 51 // Lua 运行时未初始化
	ErrLuaNoScript    ErrorCode = 52 // lua 动作缺少 script 配置
	ErrLuaExecFailed  ErrorCode = 53 // Lua 脚本执行异常
	ErrLuaScriptCheck ErrorCode = 54 // 脚本校验失败

	// ErrCallbackLua 表示 Lua 回调脚本执行失败（回调层 61-70）。
	ErrCallbackLua   ErrorCode = 61 // Lua 回调脚本执行失败
	ErrCallbackParse ErrorCode = 62 // 推送消息解析失败
)

// CodeInfo 单条错误码元数据，HTTP 端点 /sbot/api/error-codes 返回此结构。
type CodeInfo struct {
	Code uint64 `json:"code"` // 数值错误码
	Name string `json:"name"` // 大写下划线格式的错误码名称（如 "CONN_NOT_FOUND"）
}
