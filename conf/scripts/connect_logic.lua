-- connect_logic.lua: 连接逻辑服 TCP + 密钥交换
-- 心跳已迁移到声明式 tcpHeartbeat action（RegisterLogicHeartbeat，flow.json），此处不再注册。
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

    log.info("逻辑服连接成功: address=" .. tostring(logicAddress))
    return 0
end
