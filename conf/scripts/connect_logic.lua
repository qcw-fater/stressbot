-- connect_logic.lua: 连接逻辑服 TCP + 密钥交换 + 注册 5s 心跳
local network = require("network")
local robot = require("robot")
local utils = require("utils")

function execute(r)
    local logicAddress = robot.get("logicAddress") or "127.0.0.1:9001"
    utils.log_info("连接逻辑服: " .. logicAddress)

    local ok = network.connect_tcp("logic", logicAddress)
    if not ok then
        utils.log_error("连接逻辑服失败: " .. logicAddress)
        return 1
    end

    ok = network.exchange_key("logic")
    if not ok then
        utils.log_error("逻辑服密钥交换失败")
        return 1
    end

    -- 注册 5 秒心跳（Logic: cmd=2 MAIN, act=1 SERVER_TIME_CS，空 body）
    network.register_heartbeat("tcp", "logic", 5000, {cmd=2, act=1}, function()
        return ""
    end)

    utils.log_info("逻辑服连接成功 心跳已注册(5s)")
    return 0
end
