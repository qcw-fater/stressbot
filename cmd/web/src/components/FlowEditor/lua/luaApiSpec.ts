/**
 * Lua API 结构化定义。
 *
 * 这是 Monaco completion / hover / signature 三个 provider 共享的事实源；
 * 数据完全来自 stressbot/script/api_*.go 的 Lua 函数注释。
 *
 * 维护原则：
 *   - 任何 stressbot 端新增/修改 Lua 函数时，必须同步更新本文件；
 *   - 参数 type 字段是给开发者看的提示，不参与运行时校验（gopher-lua 是动态类型）；
 *   - example 中的代码片段会出现在 hover 文档中，请保证可独立运行。
 */

export interface LuaParam {
  name: string;
  type: string;
  optional?: boolean;
  /** 中文说明 */
  doc: string;
}

export interface LuaFunction {
  /** 函数名（不含模块前缀） */
  name: string;
  /** 模块名（如 'robot'） */
  module: string;
  /** 参数列表 */
  params: LuaParam[];
  /** 返回值类型描述（自由文本） */
  returns: string;
  /** 一句话功能描述 */
  summary: string;
  /** 详细说明（可多行） */
  detail?: string;
  /** 用法示例（一段 Lua 代码） */
  example?: string;
}

export interface LuaModule {
  name: string;
  summary: string;
  functions: LuaFunction[];
}

const robotModule: LuaModule = {
  name: 'robot',
  summary: '机器人状态读写、ID/账号、context 检查',
  functions: [
    {
      name: 'get',
      module: 'robot',
      params: [{ name: 'key', type: 'string', doc: '状态键名' }],
      returns: 'any | nil',
      summary: '读取状态键（返回独立 Lua 表/标量，可自由加工）',
      detail:
        '消息类 key 整树物化为 Lua 表，成本与树大小成正比。整份数据要拿来加工/修改用 get；大消息只读挑着看请用 get_view。',
      example: `local v = robot.get("playerId")`,
    },
    {
      name: 'get_view',
      module: 'robot',
      params: [{ name: 'key', type: 'string', doc: '状态键名（消息形态）' }],
      returns: 'userdata | nil',
      summary: '借出消息类状态的只读惰性视图（零物化，大消息只读首选）',
      detail:
        '返回与 await_listen 相同的 wire 视图 userdata，只能用 proto.get_field/get_path/list_size/list_get/iter_list/serialize 窄读，不支持 view.foo 表语法（误用会报错指路）。视图只读且是借出时数据的快照；key 为标量、脚本存的 Lua 表或被 set_path 改写过时报错，请改用 robot.get。范例：conf/scripts/system_shop_buy.lua。',
      example: `local view = robot.get_view("systemShopData")\nlocal n = proto.list_size(view, "shopData")`,
    },
    {
      name: 'set',
      module: 'robot',
      params: [
        { name: 'key', type: 'string', doc: '状态键名' },
        { name: 'value', type: 'any', doc: '任意值（标量 / table / proto userdata）' },
      ],
      returns: '-',
      summary: '写入状态键',
      example: `robot.set("token", token_str)`,
    },
    {
      name: 'has',
      module: 'robot',
      params: [{ name: 'key', type: 'string', doc: '状态键名' }],
      returns: 'boolean',
      summary: '检查状态键是否存在',
    },
    {
      name: 'delete',
      module: 'robot',
      params: [{ name: 'key', type: 'string', doc: '状态键名' }],
      returns: '-',
      summary: '删除单个状态键',
    },
    {
      name: 'clear',
      module: 'robot',
      params: [{ name: 'key', type: 'string', optional: true, doc: '不传则清空所有键' }],
      returns: '-',
      summary: '清空状态（可选指定单个键）',
    },
    {
      name: 'keys',
      module: 'robot',
      params: [],
      returns: 'string[]',
      summary: '返回当前所有状态键的列表',
    },
    {
      name: 'increment',
      module: 'robot',
      params: [{ name: 'key', type: 'string', doc: '状态键名' }],
      returns: 'number',
      summary: '原子递增并返回新值',
      detail: '用于生成自增 ID 等场景',
    },
    {
      name: 'get_path',
      module: 'robot',
      params: [{ name: 'path', type: 'string', doc: '形如 "a.b[0].c" 的路径' }],
      returns: 'any | nil',
      summary: '按路径访问嵌套 map/list',
      example: `local item = robot.get_path("playerData.bag[0].itemId")`,
    },
    {
      name: 'set_path',
      module: 'robot',
      params: [
        { name: 'path', type: 'string', doc: '形如 "a.b[0].c" 的路径' },
        { name: 'value', type: 'any', doc: '任意值' },
      ],
      returns: '-',
      summary: '按路径写入嵌套 map（自动创建中间节点）',
      example: `robot.set_path("playerData.bag[0].itemId", 1001)`,
    },
    {
      name: 'get_id',
      module: 'robot',
      params: [],
      returns: 'number',
      summary: '返回机器人编号（= startNumber + index）',
    },
    {
      name: 'get_index',
      module: 'robot',
      params: [],
      returns: 'number',
      summary: '返回任务全局序号（0-based，不含 startNumber 偏移）',
    },
    {
      name: 'get_account',
      module: 'robot',
      params: [],
      returns: 'string',
      summary: '返回账号名',
    },
    {
      name: 'get_context',
      module: 'robot',
      params: [],
      returns: 'boolean',
      summary: '检查 context 是否已取消（true=已取消）',
      detail: '长循环中应定期检查；为 true 时立即 return',
    },
    {
      name: 'error',
      module: 'robot',
      params: [
        { name: 'code', type: 'number', doc: '错误码（框架码 <100 / 服务端码 ≥100）' },
        { name: 'detail', type: 'string', doc: '错误详情（业务上下文）' },
      ],
      returns: 'err table {code, detail}',
      summary: '构造 err table，用于 action 脚本 return err',
      example: `return robot.error(54, "battleId 缺失")`,
    },
  ],
};

const networkModule: LuaModule = {
  name: 'network',
  summary: 'TCP/UDP 网络收发、密钥管理、监听占位',
  functions: [
    {
      name: 'connect_tcp',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名（如 "logic" / "battle"）' },
        { name: 'address', type: 'string', doc: '地址（如 "127.0.0.1:9001"）' },
      ],
      returns: 'err : nil|table',
      summary: '建立 TCP 连接',
      detail: 'err=nil 成功；err table 失败（code=6 取消 / 2 连接关闭 / ...，含 detail）。',
    },
    {
      name: 'connect_udp',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'address', type: 'string', doc: '地址' },
      ],
      returns: 'err : nil|table',
      summary: '建立 UDP 连接',
      detail: 'err=nil 成功；err table 失败（code=6 取消 / 2 连接关闭 / ...，含 detail）。',
    },
    {
      name: 'close_tcp',
      module: 'network',
      params: [{ name: 'service', type: 'string', doc: '连接名' }],
      returns: '-',
      summary: '关闭 TCP 连接',
    },
    {
      name: 'close_udp',
      module: 'network',
      params: [{ name: 'service', type: 'string', doc: '连接名' }],
      returns: '-',
      summary: '关闭 UDP 连接',
    },
    {
      name: 'tcp_request',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'route', type: 'table', doc: '路由 {cmd=N, act=M}' },
        { name: 'msg', type: 'proto userdata', doc: 'C2S 消息（proto.create 的返回值）' },
        { name: 's2c_proto', type: 'string', optional: true, doc: '响应 proto 全名' },
        { name: 'timeout_sec', type: 'number', optional: true, doc: '超时秒数；不传则使用任务 timeoutSec' },
      ],
      returns: 'err, data : (nil|table, ...|nil)',
      summary: 'TCP 请求-响应',
      detail: 'err=nil 成功（data 为响应数据）；err table 失败（含 code/detail，code 框架码 1-99 / 服务端码 ≥100）。timeout_sec 优先于任务配置。',
      example: `local err, resp = network.tcp_request("logic", {cmd=1, act=1}, msg, "login.LoginS2C")`,
    },
    {
      name: 'tcp_request_route',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'request_route', type: 'table', doc: '请求路由，用于编码发送包' },
        { name: 'response_route', type: 'table', doc: '响应路由，用于计算等待响应的 routeKey' },
        { name: 'msg', type: 'proto userdata', doc: 'C2S 消息（proto.create 的返回值）' },
        { name: 's2c_proto', type: 'string', optional: true, doc: '响应 proto 全名' },
        { name: 'timeout_sec', type: 'number', optional: true, doc: '超时秒数；不传则使用任务 timeoutSec' },
      ],
      returns: 'err, data : (nil|table, ...|nil)',
      summary: 'TCP 请求-响应（请求/响应路由分离）',
      detail: '适用于少数请求路由和响应路由不同的协议：request_route 用于编码发送，response_route 由协议配置（codec）计算 route 后用于 responseMap 匹配。不会 fallback 到请求路由。err=nil 成功（data 为响应数据）；err table 失败（含 code/detail）。',
      example: `local err, resp = network.tcp_request_route("logic", {cmd=10, act=1}, {cmd=20, act=7}, msg, "Game.SpecialS2C")`,
    },
    {
      name: 'tcp_send',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'route', type: 'table', doc: '路由 {cmd, act}' },
        { name: 'msg', type: 'proto userdata', doc: 'C2S 消息' },
      ],
      returns: 'err : nil|table',
      summary: 'TCP 发送（不等响应）',
      detail: 'err=nil 成功；err table 失败（含 code/detail）。',
    },
    {
      name: 'udp_send',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'route', type: 'table', doc: '路由 {cmd, act}' },
        { name: 'body', type: 'string', doc: '消息体字节' },
      ],
      returns: 'err : nil|table',
      summary: 'UDP 发送（带路由编码，不等响应）',
      detail: 'err=nil 成功；err table 失败（含 code/detail）。',
    },
    {
      name: 'udp_request',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'route', type: 'table', doc: '路由 {cmd=N, act=M}' },
        { name: 'body', type: 'string', doc: '消息体字节' },
        { name: 's2c_proto', type: 'string', optional: true, doc: '响应 proto 全名' },
        { name: 'timeout_sec', type: 'number', optional: true, doc: '超时秒数；不传则使用任务 timeoutSec' },
      ],
      returns: 'err, data : (nil|table, ...|nil)',
      summary: 'UDP 请求-响应',
      detail: 'err=nil 成功（data 为响应数据）；err table 失败（含 code/detail，code 框架码 1-99 / 服务端码 ≥100）。timeout_sec 优先于任务配置。',
    },
    {
      name: 'udp_request_route',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'request_route', type: 'table', doc: '请求路由，用于编码发送包' },
        { name: 'response_route', type: 'table', doc: '响应路由，用于计算等待响应的 routeKey' },
        { name: 'body', type: 'string', doc: '消息体字节' },
        { name: 's2c_proto', type: 'string', optional: true, doc: '响应 proto 全名' },
        { name: 'timeout_sec', type: 'number', optional: true, doc: '超时秒数；不传则使用任务 timeoutSec' },
      ],
      returns: 'err, data : (nil|table, ...|nil)',
      summary: 'UDP 请求-响应（请求/响应路由分离）',
      detail: '适用于少数请求路由和响应路由不同的协议：request_route 用于编码发送，response_route 由协议配置（codec）计算 route 后用于 responseMap 匹配。不会 fallback 到请求路由。err=nil 成功（data 为响应数据）；err table 失败（含 code/detail）。',
      example: `local err, resp = network.udp_request_route("battle", {cmd=10, act=1}, {cmd=20, act=7}, body, "Game.SpecialS2C")`,
    },
    {
      name: 'udp_listen',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'route', type: 'table', doc: '路由（用于计算响应键）' },
        { name: 's2c_proto', type: 'string', optional: true, doc: '响应 proto 全名' },
        { name: 'timeout_sec', type: 'number', optional: true, doc: '超时秒数（默认 60）' },
        { name: 'poll_ms', type: 'number', optional: true, doc: '轮询间隔毫秒（默认 100）' },
      ],
      returns: 'err, data : (nil|table, ...|nil)',
      summary: '等待 UDP 服务端推送（轮询 ListenRefs 预缓存）',
      detail: 'err=nil 成功（data 为响应数据，可能为 nil）；err table 失败（code=31 超时 / 6 取消 / 12 解析失败 / 其他=服务端 HeaderErr，含 detail）。需先调用 ensure_udp_listener() 注册监听占位。',
    },
    {
      name: 'http_request',
      module: 'network',
      params: [
        { name: 'url', type: 'string', doc: '完整请求 URL' },
        { name: 'method', type: 'string', optional: true, doc: 'HTTP 方法（默认 "POST"）' },
        { name: 'content_type', type: 'string', optional: true, doc: '"json" / "form"（默认 "form"）' },
        { name: 'body', type: 'table', optional: true, doc: '请求体 table' },
      ],
      returns: 'err, status, body : (nil|table, number, string)',
      summary: 'HTTP 请求',
      detail: 'err=nil 表示拿到响应（status 是 HTTP 状态码，不是错误）；err table 表示框架传输错误（code 1-99，含 detail）。',
    },
    {
      name: 'tcp_listen',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'route', type: 'table', doc: '路由（用于计算响应键）' },
        { name: 's2c_proto', type: 'string', optional: true, doc: '响应 proto 全名' },
        { name: 'timeout_sec', type: 'number', optional: true, doc: '超时秒数（默认 60）' },
        { name: 'poll_ms', type: 'number', optional: true, doc: '轮询间隔毫秒（默认 100）' },
      ],
      returns: 'err, data : (nil|table, ...|nil)',
      summary: '等待 TCP 服务端推送（轮询 ListenRefs 预缓存）',
      detail: 'err=nil 成功（data 为响应数据，可能为 nil）；err table 失败（code=31 超时 / 6 取消 / 12 解析失败 / 其他=服务端 HeaderErr，含 detail）。需先调用 ensure_tcp_listener() 注册监听占位。等待期间只阻塞当前机器人主流程；连接收包与声明式心跳会继续独立运行。',
    },
    {
      name: 'try_tcp_listen',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'route', type: 'table', doc: '路由（用于计算响应键）' },
      ],
      returns: 'err, data : (nil|table, string|nil)',
      summary: '非阻塞单次 pop 最新 TCP 监听消息（不解析 proto）',
      detail: '单次非阻塞 pop，不轮询、不 sleep。err=nil 成功（data 可能=nil 表示队列空、无新消息；data=string 表示取到原始 body，**不解析 proto**）；err table 失败（编码/routeKey 解析失败或服务端 HeaderErr，含 detail）。需 proto 解析请用阻塞版 tcp_listen。',
    },
    {
      name: 'try_udp_listen',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'route', type: 'table', doc: '路由（用于计算响应键）' },
      ],
      returns: 'err, data : (nil|table, string|nil)',
      summary: '非阻塞单次 pop 最新 UDP 监听消息（不解析 proto）',
      detail: '返回语义同 try_tcp_listen。适用于高频 sync loop「保最新」消费场景（如 battleAck 追踪）。',
    },
    {
      name: 'set_tcp_secret_key',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'key', type: 'string', doc: '密钥字节串' },
      ],
      returns: '-',
      summary: '设置 TCP 加密密钥',
    },
    {
      name: 'set_udp_secret_key',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'key', type: 'string', doc: '密钥字节串' },
      ],
      returns: '-',
      summary: '设置 UDP 加密密钥',
    },
    {
      name: 'get_tcp_secret_key',
      module: 'network',
      params: [{ name: 'service', type: 'string', doc: '连接名' }],
      returns: 'string | nil',
      summary: '读取 TCP 加密密钥',
    },
    {
      name: 'get_udp_secret_key',
      module: 'network',
      params: [{ name: 'service', type: 'string', doc: '连接名' }],
      returns: 'string | nil',
      summary: '读取 UDP 加密密钥',
    },
    {
      name: 'ensure_tcp_listener',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'response_key', type: 'string', doc: '响应路由键' },
      ],
      returns: '-',
      summary: '为 TCP responseKey 注册监听占位',
    },
    {
      name: 'ensure_udp_listener',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'response_key', type: 'string', doc: '响应路由键' },
      ],
      returns: '-',
      summary: '为 UDP responseKey 注册监听占位',
    },
  ],
};

const protoModule: LuaModule = {
  name: 'proto',
  summary: '动态 protobuf 创建 / 字段读写 / 序列化',
  functions: [
    {
      name: 'create',
      module: 'proto',
      params: [{ name: 'name', type: 'string', doc: '消息全名（如 "login.LoginC2S"）' }],
      returns: 'userdata (proto message)',
      summary: '创建空 proto 消息',
      example: `local msg = proto.create("login.LoginC2S")`,
    },
    {
      name: 'set_field',
      module: 'proto',
      params: [
        { name: 'msg', type: 'userdata', doc: 'proto 消息' },
        { name: 'field', type: 'string', doc: '字段名' },
        { name: 'value', type: 'any', doc: '值（标量 / table / 嵌套 proto）' },
      ],
      returns: '-',
      summary: '设置字段值',
    },
    {
      name: 'get_field',
      module: 'proto',
      params: [
        { name: 'msg', type: 'userdata', doc: 'proto 消息' },
        { name: 'field', type: 'string', doc: '字段名' },
      ],
      returns: 'any',
      summary: '读取字段值',
    },
    {
      name: 'get_path',
      module: 'proto',
      params: [
        { name: 'msg', type: 'userdata', doc: 'proto 消息' },
        { name: 'path', type: 'string', doc: '字段路径，支持点分路径和数组索引' },
      ],
      returns: 'any',
      summary: '按字段路径读取值',
      detail: '后端注册为 proto.get_field 的别名，也支持 msg:get_path(path) 方法调用。',
    },
    {
      name: 'get_field_map',
      module: 'proto',
      params: [{ name: 'msg', type: 'userdata', doc: 'proto 消息' }],
      returns: 'table',
      summary: '把所有字段以 table 返回',
    },
    {
      name: 'serialize',
      module: 'proto',
      params: [{ name: 'msg', type: 'userdata', doc: 'proto 消息' }],
      returns: 'string',
      summary: '序列化为字节串',
    },
    {
      name: 'parse',
      module: 'proto',
      params: [
        { name: 'name', type: 'string', doc: '消息全名' },
        { name: 'data', type: 'string', doc: '字节串' },
      ],
      returns: 'userdata',
      summary: '反序列化为消息对象',
    },
    {
      name: 'iter_list',
      module: 'proto',
      params: [
        { name: 'msg', type: 'userdata', doc: 'proto 消息' },
        { name: 'field', type: 'string', doc: 'list 字段名' },
      ],
      returns: 'iterator',
      summary: '顺序遍历 repeated 字段（大列表首选，wire 视图上为 O(n) 游标）',
      detail:
        'message 元素产出为 userdata（继续用 proto.get_field 等读取），标量元素为值。字段不存在返回 nil，非 repeated 字段空迭代。',
      example: `for i, item in proto.iter_list(msg, "items") do\n  local id = proto.get_field(item, "id")\nend`,
    },
    {
      name: 'list_size',
      module: 'proto',
      params: [
        { name: 'msg', type: 'userdata', doc: 'proto 消息' },
        { name: 'field', type: 'string', doc: 'list 字段名' },
      ],
      returns: 'number',
      summary: 'repeated 字段长度',
    },
    {
      name: 'list_get',
      module: 'proto',
      params: [
        { name: 'msg', type: 'userdata', doc: 'proto 消息' },
        { name: 'field', type: 'string', doc: 'list 字段名' },
        { name: 'idx', type: 'number', doc: '1-based 索引' },
      ],
      returns: 'any',
      summary: '取 repeated 字段第 idx 项',
    },
  ],
};

const utilsModule: LuaModule = {
  name: 'utils',
  summary: '随机化、时间、二进制打包、加权选择等工具',
  functions: [
    {
      name: 'random_int',
      module: 'utils',
      params: [{ name: 'n', type: 'number', doc: '上限（不含）' }],
      returns: 'number',
      summary: '返回 [0, n-1] 随机整数',
    },
    {
      name: 'rand_range',
      module: 'utils',
      params: [
        { name: 'lo', type: 'number', doc: '下限（含）' },
        { name: 'hi', type: 'number', doc: '上限（含）' },
      ],
      returns: 'number',
      summary: '返回 [lo, hi] 闭区间随机整数',
    },
    {
      name: 'random_bool',
      module: 'utils',
      params: [],
      returns: 'boolean',
      summary: '随机布尔',
    },
    {
      name: 'random_string',
      module: 'utils',
      params: [{ name: 'length', type: 'number', doc: '长度（默认 8）' }],
      returns: 'string',
      summary: '随机字母数字串',
    },
    {
      name: 'random_pick',
      module: 'utils',
      params: [{ name: 'arr', type: 'table', doc: '数组' }],
      returns: 'any',
      summary: '从数组随机选一个',
    },
    {
      name: 'random_pick_n',
      module: 'utils',
      params: [
        { name: 'arr', type: 'table', doc: '数组' },
        { name: 'n', type: 'number', doc: '挑选数量' },
      ],
      returns: 'table',
      summary: '从数组随机选 N 个（不重复）',
    },
    {
      name: 'weighted_pick',
      module: 'utils',
      params: [
        { name: 'items', type: 'table', doc: '元素数组' },
        { name: 'weights', type: 'table', doc: '权重数组（同长度）' },
      ],
      returns: 'item, idx : (any, number)',
      summary: '加权随机',
    },
    {
      name: 'rand_filter',
      module: 'utils',
      params: [
        { name: 'items', type: 'table', doc: '元素数组' },
        { name: 'excludes', type: 'table', optional: true, doc: '排除集合' },
        { name: 'count', type: 'number', optional: true, doc: '数量（默认 1）' },
      ],
      returns: 'table',
      summary: '排除后随机选 N 个',
    },
    {
      name: 'rand_filter_one',
      module: 'utils',
      params: [
        { name: 'items', type: 'table', doc: '元素数组' },
        { name: 'excludes', type: 'table', optional: true, doc: '排除集合' },
      ],
      returns: 'any | nil',
      summary: '排除后随机选 1 个',
    },
    {
      name: 'sleep',
      module: 'utils',
      params: [{ name: 'ms', type: 'number', doc: '毫秒数' }],
      returns: '-',
      summary: '休眠（响应 context 取消，仅暂停当前机器人主流程）',
    },
    {
      name: 'time_ms',
      module: 'utils',
      params: [],
      returns: 'number',
      summary: '当前 Unix 毫秒时间戳',
    },
    {
      name: 'fnv_hash',
      module: 'utils',
      params: [{ name: 'version', type: 'string', doc: '原始字符串' }],
      returns: 'string',
      summary: 'FNV-1a 64位哈希（hex string）',
    },
    {
      name: 'pack_le',
      module: 'utils',
      params: [
        { name: 'format', type: 'string', doc: 'u8/i8/u16/i16/u32/i32/u64/i64/f32/f64' },
        { name: '...', type: 'number | string', doc: '值列表（u64/i64 支持字符串大数）' },
      ],
      returns: 'string',
      summary: '小端二进制打包',
    },
    {
      name: 'unpack_le',
      module: 'utils',
      params: [
        { name: 'data', type: 'string', doc: '二进制字符串（pack_le 产物）' },
        { name: 'fmt1', type: 'string', doc: '第一个字段的格式' },
        { name: '...', type: 'string', doc: '后续字段格式（可变）' },
      ],
      returns: 'number | string ...',
      summary: '小端二进制解包',
      detail: 'i64/u64 超过 2^53 时返回字符串；按格式顺序返回多个值',
      example: `local idx, ts = utils.unpack_le(data, "u16", "i64")`,
    },
    {
      name: 'shuffle',
      module: 'utils',
      params: [{ name: 'arr', type: 'table', doc: '数组（原地打乱）' }],
      returns: 'table',
      summary: '原地随机打乱数组',
      example: `local order = utils.shuffle({1,2,3,4,5})`,
    },
  ],
};

const jsonModule: LuaModule = {
  name: 'json',
  summary: 'JSON 编解码',
  functions: [
    {
      name: 'encode',
      module: 'json',
      params: [{ name: 'value', type: 'any', doc: '要序列化的值' }],
      returns: 'string',
      summary: '编码为 JSON 字符串',
    },
    {
      name: 'decode',
      module: 'json',
      params: [{ name: 'str', type: 'string', doc: 'JSON 字符串' }],
      returns: 'any',
      summary: '解码为 Lua 值',
    },
  ],
};

const logModule: LuaModule = {
  name: 'log',
  summary: '日志输出（自动附带 robot id / account 前缀）',
  functions: [
    {
      name: 'debug',
      module: 'log',
      params: [{ name: 'msg', type: 'string', doc: '日志内容' }],
      returns: '-',
      summary: 'DEBUG 级别',
    },
    {
      name: 'info',
      module: 'log',
      params: [{ name: 'msg', type: 'string', doc: '日志内容' }],
      returns: '-',
      summary: 'INFO 级别',
    },
    {
      name: 'warn',
      module: 'log',
      params: [{ name: 'msg', type: 'string', doc: '日志内容' }],
      returns: '-',
      summary: 'WARN 级别',
    },
    {
      name: 'error',
      module: 'log',
      params: [{ name: 'msg', type: 'string', doc: '日志内容' }],
      returns: '-',
      summary: 'ERROR 级别',
    },
  ],
};

const shareModule: LuaModule = {
  name: 'share',
  summary: '跨机器人 / 跨节点共享状态（Redis），用于协作型压测',
  functions: [
    {
      name: 'set',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: '键名（自动按任务隔离）' },
        { name: 'value', type: 'any', doc: '标量 / 数组 / map（JSON 可序列化）' },
        { name: 'ttl_sec', type: 'number', optional: true, doc: '过期秒数；不传=不过期' },
      ],
      returns: 'ok, err : (boolean, string|nil)',
      summary: '写入共享键值',
      detail: '所有 share.* 返回 (值或ok, err)；err 非 nil 表示出错。未配置 Redis 时返回明确错误。',
      example: `local ok, err = share.set("leader", robot.get_account(), 60)`,
    },
    {
      name: 'get',
      module: 'share',
      params: [{ name: 'key', type: 'string', doc: '键名' }],
      returns: 'value, ok, err : (any|nil, boolean, string|nil)',
      summary: '读取共享键值（ok=false 表示不存在）',
      detail: 'ok 用于区分「键不存在」与「存储了 null」；常见用法 local v = share.get(k) 仍可只取第一个值。',
      example: `local v, ok, err = share.get("leader")`,
    },
    {
      name: 'del',
      module: 'share',
      params: [{ name: 'key', type: 'string', doc: '键名' }],
      returns: 'ok, err : (boolean, string|nil)',
      summary: '删除共享键（作用于该键的所有数据类型）',
      detail: '跨命名空间删除：清掉该 key 名下的 kv/计数器/队列/hash 全部数据（不影响 claim 锁）。',
    },
    {
      name: 'exists',
      module: 'share',
      params: [{ name: 'key', type: 'string', doc: '键名' }],
      returns: 'exists, err : (boolean, string|nil)',
      summary: '判断键是否存在（任一数据类型存在即 true）',
      detail: '检查该 key 名下的 kv/计数器/队列/hash 是否存在（不含 claim 锁）。',
    },
    {
      name: 'expire',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: '键名' },
        { name: 'ttl_sec', type: 'number', doc: '过期秒数' },
      ],
      returns: 'ok, err : (boolean, string|nil)',
      summary: '设置键过期时间（作用于该键的所有数据类型）',
      detail: '为该 key 名下的 kv/计数器/队列/hash 统一设置 TTL（不含 claim 锁）。',
    },
    {
      name: 'incr',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: '计数器键名' },
        { name: 'delta', type: 'number', optional: true, doc: '增量（默认 1）' },
        { name: 'ttl_sec', type: 'number', optional: true, doc: '过期秒数' },
      ],
      returns: 'value, err : (number, string|nil)',
      summary: '原子递增计数器并返回新值',
      example: `local n, err = share.incr("ready_count")`,
    },
    {
      name: 'claim',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: '资源键名' },
        { name: 'owner', type: 'string', doc: '占用者标识（如账号名）' },
        { name: 'ttl_sec', type: 'number', optional: true, doc: '租约秒数；不传=服务器默认' },
      ],
      returns: 'ok, err : (boolean, string|nil)',
      summary: '抢占资源/分布式锁（成功返回 true）',
      detail: '原子 SET NX：仅当资源未被占用时成功。配合 renew/release 使用。',
      example: `if share.claim("room:1", robot.get_account(), 30) then
  -- 抢到房主
end`,
    },
    {
      name: 'release',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: '资源键名' },
        { name: 'owner', type: 'string', doc: '占用者标识（须与 claim 一致）' },
      ],
      returns: 'released, err : (boolean, string|nil)',
      summary: '释放自己持有的资源',
    },
    {
      name: 'owner',
      module: 'share',
      params: [{ name: 'key', type: 'string', doc: '资源键名' }],
      returns: 'owner, err : (string|nil, string|nil)',
      summary: '查询资源当前持有者',
    },
    {
      name: 'renew',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: '资源键名' },
        { name: 'owner', type: 'string', doc: '占用者标识' },
        { name: 'ttl_sec', type: 'number', optional: true, doc: '续租秒数' },
      ],
      returns: 'ok, err : (boolean, string|nil)',
      summary: '续租自己持有的资源',
    },
    {
      name: 'queue_push',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: '队列键名' },
        { name: 'value', type: 'any', doc: '入队元素' },
        { name: 'ttl_sec', type: 'number', optional: true, doc: '过期秒数' },
      ],
      returns: 'ok, err : (boolean, string|nil)',
      summary: '尾部入队（FIFO）',
    },
    {
      name: 'queue_pop',
      module: 'share',
      params: [{ name: 'key', type: 'string', doc: '队列键名' }],
      returns: 'value, ok, err : (any|nil, boolean, string|nil)',
      summary: '头部出队（ok=false 表示队列为空）',
      detail: 'ok 用于区分「空队列」与「出队了 null 元素」。',
      example: `local v, ok = share.queue_pop("jobs")
if ok then -- 拿到一个任务 end`,
    },
    {
      name: 'queue_len',
      module: 'share',
      params: [{ name: 'key', type: 'string', doc: '队列键名' }],
      returns: 'n, err : (number, string|nil)',
      summary: '队列长度',
    },
    {
      name: 'queue_expire',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: '队列键名' },
        { name: 'ttl_sec', type: 'number', doc: '过期秒数' },
      ],
      returns: 'ok, err : (boolean, string|nil)',
      summary: '设置队列过期时间',
    },
    {
      name: 'hash_set',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: 'hash 键名' },
        { name: 'field', type: 'string', doc: '字段名' },
        { name: 'value', type: 'any', doc: '字段值' },
        { name: 'ttl_sec', type: 'number', optional: true, doc: '过期秒数' },
      ],
      returns: 'ok, err : (boolean, string|nil)',
      summary: '写入 hash 字段',
    },
    {
      name: 'hash_get',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: 'hash 键名' },
        { name: 'field', type: 'string', doc: '字段名' },
      ],
      returns: 'value, ok, err : (any|nil, boolean, string|nil)',
      summary: '读取 hash 字段（ok=false 表示字段不存在）',
    },
    {
      name: 'hash_get_all',
      module: 'share',
      params: [{ name: 'key', type: 'string', doc: 'hash 键名' }],
      returns: 'table, err : (table|nil, string|nil)',
      summary: '读取整个 hash（不存在返回 nil）',
    },
    {
      name: 'hash_del',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: 'hash 键名' },
        { name: 'field', type: 'string', doc: '字段名' },
      ],
      returns: 'ok, err : (boolean, string|nil)',
      summary: '删除 hash 字段',
    },
    {
      name: 'hash_incr',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: 'hash 键名' },
        { name: 'field', type: 'string', doc: '字段名' },
        { name: 'delta', type: 'number', optional: true, doc: '增量（默认 1）' },
        { name: 'ttl_sec', type: 'number', optional: true, doc: '过期秒数' },
      ],
      returns: 'value, err : (number, string|nil)',
      summary: '原子递增 hash 字段并返回新值',
    },
    {
      name: 'hash_expire',
      module: 'share',
      params: [
        { name: 'key', type: 'string', doc: 'hash 键名' },
        { name: 'ttl_sec', type: 'number', doc: '过期秒数' },
      ],
      returns: 'ok, err : (boolean, string|nil)',
      summary: '设置 hash 过期时间',
    },
  ],
};

export const LUA_MODULES: readonly LuaModule[] = [
  robotModule,
  networkModule,
  protoModule,
  utilsModule,
  jsonModule,
  logModule,
  shareModule,
];

const moduleByName = new Map(LUA_MODULES.map((m) => [m.name, m]));

export function getLuaModule(name: string): LuaModule | undefined {
  return moduleByName.get(name);
}

export function getLuaFunction(module: string, name: string): LuaFunction | undefined {
  return getLuaModule(module)?.functions.find((f) => f.name === name);
}

/** 渲染参数列表为 Lua 函数签名字符串：`(key, value [, opt])` */
export function renderSignature(fn: LuaFunction): string {
  const parts = fn.params.map((p) => (p.optional ? `[${p.name}]` : p.name));
  return `(${parts.join(', ')})`;
}

/** 渲染单个参数为 Markdown：`**key** _string_ — 状态键名` */
export function renderParam(p: LuaParam): string {
  const tag = p.optional ? ` _opt_` : '';
  return `- **${p.name}** _${p.type}_${tag} — ${p.doc}`;
}

/** 渲染完整 hover 文档（Markdown） */
export function renderDoc(fn: LuaFunction): string {
  const lines: string[] = [];
  lines.push(`\`\`\`lua`);
  lines.push(`${fn.module}.${fn.name}${renderSignature(fn)} → ${fn.returns}`);
  lines.push(`\`\`\``);
  lines.push('');
  lines.push(fn.summary);
  if (fn.detail) {
    lines.push('');
    lines.push(fn.detail);
  }
  if (fn.params.length > 0) {
    lines.push('');
    lines.push('**参数**：');
    for (const p of fn.params) lines.push(renderParam(p));
  }
  if (fn.example) {
    lines.push('');
    lines.push('**示例**：');
    lines.push('```lua');
    lines.push(fn.example);
    lines.push('```');
  }
  return lines.join('\n');
}
