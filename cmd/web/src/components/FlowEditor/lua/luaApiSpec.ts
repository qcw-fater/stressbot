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
      summary: '读取状态键',
      example: `local v = robot.get("playerId")`,
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
      name: 'get_id',
      module: 'robot',
      params: [],
      returns: 'number',
      summary: '返回机器人编号',
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
  ],
};

const networkModule: LuaModule = {
  name: 'network',
  summary: 'TCP/UDP 网络收发、密钥管理、心跳注册',
  functions: [
    {
      name: 'connect_tcp',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名（如 "logic" / "battle"）' },
        { name: 'address', type: 'string', doc: '地址（如 "127.0.0.1:9001"）' },
      ],
      returns: 'boolean',
      summary: '建立 TCP 连接',
    },
    {
      name: 'connect_udp',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'address', type: 'string', doc: '地址' },
      ],
      returns: 'boolean',
      summary: '建立 UDP 连接',
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
      ],
      returns: 'code, data, sent, recv : (number, string|userdata|nil, number, number)',
      summary: 'TCP 请求-响应',
      detail: 'code: 0=成功 / -1=请求失败 / -2=解析失败（data 为原始字节）',
      example: `local code, resp = network.tcp_request("logic", {cmd=1, act=1}, msg, "login.LoginS2C")`,
    },
    {
      name: 'tcp_send',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'route', type: 'table', doc: '路由 {cmd, act}' },
        { name: 'msg', type: 'proto userdata', doc: 'C2S 消息' },
      ],
      returns: 'code, sent : (number, number)',
      summary: 'TCP 发送（不等响应）',
      detail: 'code: 0=成功 / -1=失败',
    },
    {
      name: 'udp_send',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'route', type: 'table', doc: '路由 {cmd, act}' },
        { name: 'body', type: 'string', doc: '消息体字节' },
      ],
      returns: 'code, sent : (number, number)',
      summary: 'UDP 发送（带路由编码，不等响应）',
    },
    {
      name: 'udp_request',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'route', type: 'table', doc: '路由 {cmd=N, act=M}' },
        { name: 'body', type: 'string', doc: '消息体字节' },
        { name: 's2c_proto', type: 'string', optional: true, doc: '响应 proto 全名' },
        { name: 'timeout_sec', type: 'number', optional: true, doc: '超时秒数（默认 10）' },
        { name: 'poll_ms', type: 'number', optional: true, doc: '轮询间隔毫秒（默认 100）' },
      ],
      returns: 'code, data, sent, recv : (number, string|userdata|nil, number, number)',
      summary: 'UDP 请求-响应',
      detail: 'code: 0=成功 / -1=请求失败或超时 / -2=解析失败',
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
      returns: 'data, recv : (string|userdata|nil, number)',
      summary: '等待 UDP 服务端推送（轮询）',
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
      returns: 'status_code, body, sent, recv : (number, string, number, number)',
      summary: 'HTTP 请求',
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
      returns: 'data, recv : (string|userdata|nil, number)',
      summary: '等待 TCP 服务端推送（轮询）',
      detail: 'nil 表示超时或 context 取消；阻塞期间会释放 luaMu 让心跳/回调继续',
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
    {
      name: 'register_tcp_heartbeat',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'interval_ms', type: 'number', doc: '心跳间隔（毫秒）' },
        { name: 'route', type: 'table', doc: '路由 {cmd, act}' },
        { name: 'builder', type: 'function', optional: true, doc: '可选 body 构造器，返回 string' },
      ],
      returns: '-',
      summary: '注册 TCP 心跳',
      example: `network.register_tcp_heartbeat("logic", 3000, {cmd=0, act=1}, function() return "" end)`,
    },
    {
      name: 'register_udp_heartbeat',
      module: 'network',
      params: [
        { name: 'service', type: 'string', doc: '连接名' },
        { name: 'interval_ms', type: 'number', doc: '心跳间隔（毫秒）' },
        { name: 'route', type: 'table', doc: '路由 {cmd, act}' },
        { name: 'builder', type: 'function', optional: true, doc: '可选 body 构造器' },
      ],
      returns: '-',
      summary: '注册 UDP 心跳',
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
      summary: '遍历 repeated 字段',
      example: `for i, item in proto.iter_list(msg, "items") do print(i, item) end`,
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
      summary: '休眠（响应 context 取消，自动释放 luaMu）',
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

const adapterModule: LuaModule = {
  name: 'adapter',
  summary: '编解码适配器（高级用法，通常不直接调用）',
  functions: [
    {
      name: 'encode_tcp',
      module: 'adapter',
      params: [
        { name: 'route', type: 'table', doc: '路由' },
        { name: 'body', type: 'string', doc: '消息体' },
        { name: 'key', type: 'string', optional: true, doc: '加密密钥' },
      ],
      returns: 'string | nil',
      summary: 'TCP 编码',
    },
    {
      name: 'encode_udp',
      module: 'adapter',
      params: [
        { name: 'route', type: 'table', doc: '路由' },
        { name: 'body', type: 'string', doc: '消息体' },
        { name: 'key', type: 'string', optional: true, doc: '加密密钥' },
      ],
      returns: 'string | nil',
      summary: 'UDP 编码',
    },
    {
      name: 'decode_tcp',
      module: 'adapter',
      params: [
        { name: 'data', type: 'string', doc: '原始数据' },
        { name: 'key', type: 'string', optional: true, doc: '解密密钥' },
      ],
      returns: 'response_key, body, header_err : (string, string, number)',
      summary: 'TCP 解码',
    },
    {
      name: 'decode_udp',
      module: 'adapter',
      params: [
        { name: 'data', type: 'string', doc: '原始数据' },
        { name: 'key', type: 'string', optional: true, doc: '解密密钥' },
      ],
      returns: 'response_key, body, header_err : (string, string, number)',
      summary: 'UDP 解码',
    },
    {
      name: 'expected_route_key',
      module: 'adapter',
      params: [{ name: 'route', type: 'table', doc: '路由' }],
      returns: 'string',
      summary: '由路由计算预期响应键',
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
  adapterModule,
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
