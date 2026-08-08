-- connect_sdc_battle_udp.lua: 为搜打撤建立独立别名的战斗服 UDP 连接并设置密钥。
-- SDC 使用独立的 5 秒 UDP 心跳并发送 flow 声明的低频帧，兼顾首包保活与结算窗口限流。
local network = require("network")
local robot = require("robot")
local log = require("log")

local SERVICE = "sdcbattle"

function execute(r)
    local battleAddress = robot.get("battleAddress")
    local battleId = robot.get("battleId") or 0
    local fighterIndex = robot.get("fighterIndex")
    if not battleAddress or battleAddress == "" then
        return robot.error(41, "ConnectBattleUDP 缺少战斗服地址: battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " battleSession=" .. tostring(robot.get("battleSession")))
    end

    local err = network.connect_udp(SERVICE, battleAddress)
    if err then
        log.error("搜打撤战斗服 UDP 连接失败: address=" .. tostring(battleAddress)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    local key = robot.get("battleSecretKey")
    if not key then
        return robot.error(54, "ConnectBattleUDP 缺少战斗服 UDP 密钥: battleId=" .. tostring(battleId))
    end
    network.set_udp_secret_key(SERVICE, key)

    robot.set("packageIndex", 0)
    robot.set("battleAck", 0)
    robot.set("frameCount", 0)
    log.info("搜打撤战斗服 UDP 连接完成: address=" .. tostring(battleAddress)
        .. " battleId=" .. tostring(battleId)
        .. " fighterIndex=" .. tostring(fighterIndex)
        .. " udpHeartbeatMs=5000 frameIntervalMs=5000~6000")
    return nil
end
