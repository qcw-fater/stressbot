-- connect_battle_udp.lua: 连接战斗服 UDP + 设置 UDP 密钥
-- 仅做连接 / 配置；150ms 心跳由 flow.json 的 RegisterBattleUDPHeartbeat
-- (udpHeartbeat action, raw-binary heartbeatFields) 声明式注册，后台 goroutine
-- 通过 network 层全局带宽统计，完全不触碰 robot 业务 LState。
local network = require("network")
local robot = require("robot")
local log = require("log")

function execute(r)
    local battleAddress = robot.get("battleAddress")
    local battleId = robot.get("battleId") or 0
    local fighterIndex = robot.get("fighterIndex")
    if not battleAddress or battleAddress == "" then
        log.error("ConnectBattleUDP 缺少战斗服地址: battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " battleSession=" .. tostring(robot.get("battleSession")))
        return robot.error(41, "ConnectBattleUDP 缺少战斗服地址: battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " battleSession=" .. tostring(robot.get("battleSession")))  -- 41=ADDR_EMPTY
    end

    log.info("连接战斗服 UDP: address=" .. tostring(battleAddress)
        .. " battleId=" .. tostring(battleId)
        .. " fighterIndex=" .. tostring(fighterIndex))

    local err = network.connect_udp("battle", battleAddress)
    if err then
        log.error("ConnectBattleUDP 连接失败: address=" .. tostring(battleAddress)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    -- 设置 UDP 密钥（从 listen_start_loading 保存）
    local key = robot.get("battleSecretKey")
    local hasSecretKey = key ~= nil
    if key then
        network.set_udp_secret_key("battle", key)
    else
        log.warn("战斗服 UDP 密钥为空，继续连接但后续包可能失败: battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " address=" .. tostring(battleAddress))
    end

    -- 新一轮战斗开始：复位包序号（对齐旧工具 ClearBattleInfo → packetIndex=0）
    -- 心跳的 udp_seq 私有计数器由 RegisterBattleUDPHeartbeat 节点每 battle 重新注册时从 start=0 开始，等价复位。
    robot.set("packageIndex", 0)
    robot.set("battleAck", 0)
    robot.set("frameCount", 0)

    log.info("战斗服 UDP 连接完成: address=" .. tostring(battleAddress)
        .. " battleId=" .. tostring(battleId)
        .. " fighterIndex=" .. tostring(fighterIndex)
        .. " hasSecretKey=" .. tostring(hasSecretKey)
        .. " 心跳由声明式节点注册(150ms)")
    return nil
end
