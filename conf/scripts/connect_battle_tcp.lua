-- connect_battle_tcp.lua: 连接战斗服 TCP + 密钥交换
-- 10s 心跳由 flow.json 的 RegisterBattleTCPHeartbeat
-- (tcpHeartbeat action, raw-binary heartbeatFields) 声明式注册，后台 goroutine
-- 通过 network 层全局带宽统计，完全不触碰 robot 业务 LState。
local network = require("network")
local robot = require("robot")
local log = require("log")

function execute(r)
    local battleAddress = robot.get("battleAddress")
    local battleId = robot.get("battleId") or 0
    local fighterIndex = robot.get("fighterIndex")
    local battleSession = robot.get("battleSession") or 0

    if not battleAddress or battleAddress == "" then
        log.error("ConnectBattleTCP 缺少战斗服地址: battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " battleSession=" .. tostring(battleSession))
        return robot.error(41, "ConnectBattleTCP 缺少战斗服地址: battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " battleSession=" .. tostring(battleSession))  -- 41=ADDR_EMPTY
    end

    log.info("连接战斗服 TCP: address=" .. tostring(battleAddress)
        .. " battleId=" .. tostring(battleId)
        .. " fighterIndex=" .. tostring(fighterIndex))

    local err = network.connect_tcp("battle", battleAddress)
    if err then
        log.error("连接战斗服 TCP 失败: address=" .. tostring(battleAddress)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    -- 发送空包获取密钥并设置到连接
    -- 不显式传 timeout，复用 robotConfig.timeoutSec（默认 60s）；
    -- 若需要单独缩短此握手超时，再传第 5 个参数。
    local err, keyBody = network.tcp_request("battle", {cmd=0, act=0})
    if err then
        log.error("战斗服密钥交换失败: address=" .. tostring(battleAddress)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err  -- 透传底层 err table
    end
    if not keyBody or #keyBody == 0 then
        log.error("战斗服密钥交换响应为空: address=" .. tostring(battleAddress)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex))
        return robot.error(12, "battle 密钥交换响应为空: address=" .. tostring(battleAddress)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex))  -- 12=PARSE_FAILED：协议层异常
    end
    network.set_tcp_secret_key("battle", keyBody)

    log.info("战斗服 TCP 连接成功: address=" .. tostring(battleAddress)
        .. " battleId=" .. tostring(battleId)
        .. " fighterIndex=" .. tostring(fighterIndex)
        .. " hasSecretKey=true 心跳由声明式节点注册(10s)")
    return nil
end
