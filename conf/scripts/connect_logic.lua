-- connect_logic.lua: 连接逻辑服 TCP + 密钥交换 + 注册 5s 心跳
local network = require("network")
local robot = require("robot")
local log = require("log")

function execute(r)
    local logicAddress = robot.get("logicAddress") or "127.0.0.1:9001"
    log.info("连接逻辑服: address=" .. tostring(logicAddress))

    local code = network.connect_tcp("logic", logicAddress)
    if code ~= 0 then
        log.error("连接逻辑服失败: address=" .. tostring(logicAddress)
            .. " code=" .. tostring(code))
        return code
    end

    -- 发送空包获取密钥并设置到连接
    -- 不显式传 timeout，复用 robotConfig.timeoutSec（默认 60s）；
    -- 若需要单独缩短此握手超时，再传第 5 个参数。
    local code, keyBody = network.tcp_request("logic", {cmd=0, act=0})
    if code ~= 0 then
        log.error("逻辑服密钥交换失败: address=" .. tostring(logicAddress)
            .. " code=" .. tostring(code))
        return code  -- 透传底层 code（如 5=CONN_DROPPED / 4=RECV_TIMEOUT）
    end
    if not keyBody or #keyBody == 0 then
        log.error("逻辑服密钥交换响应为空: address=" .. tostring(logicAddress))
        return 54  -- 54=LUA_EXIT_CODE：协议层异常
    end
    network.set_tcp_secret_key("logic", keyBody)

    -- 注册 5 秒心跳（Logic: cmd=2 MAIN, act=1 SERVER_TIME_CS，空 body）
    -- 不传 builder 参数 → 静态心跳，注册时一次性预编码，运行时零 Lua 开销。
    local hbCode = network.register_tcp_heartbeat("logic", 5000, {cmd=2, act=1})
    if hbCode ~= 0 then
        log.error("逻辑服心跳注册失败: service=logic route=2:1 intervalMs=5000 address="
            .. tostring(logicAddress) .. " code=" .. tostring(hbCode))
        return hbCode
    end

    log.info("逻辑服连接成功: address=" .. tostring(logicAddress) .. " 心跳已注册(5s)")
    return 0
end
