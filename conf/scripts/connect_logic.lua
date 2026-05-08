-- connect_logic.lua: 连接逻辑服 TCP + 密钥交换 + 注册 5s 心跳
local network = require("network")
local robot = require("robot")
local log = require("log")

function execute(r)
    local logicAddress = robot.get("logicAddress") or "127.0.0.1:9001"
    log.info("连接逻辑服: " .. logicAddress)

    local ok = network.connect_tcp("logic", logicAddress)
    if not ok then
        log.error("连接逻辑服失败: " .. logicAddress)
        return 1, 0, 0
    end

    -- exchange_key 在 wire 层有真实收发：sent/recv 由 lua API 返回
    local okExch, sent, recv = network.exchange_key("logic")
    if not okExch then
        log.error("逻辑服密钥交换失败")
        return 1, sent, recv
    end

    -- 注册 5 秒心跳（Logic: cmd=2 MAIN, act=1 SERVER_TIME_CS，空 body）
    -- 心跳由后台 goroutine 触发，字节由 network 层全局带宽统计，不计入本 action
    network.register_tcp_heartbeat("logic", 5000, {cmd=2, act=1}, function()
        return ""
    end)

    log.info("逻辑服连接成功 心跳已注册(5s)")
    return 0, sent, recv
end
