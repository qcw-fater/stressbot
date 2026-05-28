-- connect_battle_udp.lua: 连接战斗服 UDP + 设置 UDP 密钥 + 注册 150ms 心跳
-- 仅做连接 / 配置 / 注册心跳，无主动收发，所以 send/recv = 0（心跳字节由后台 goroutine
-- 通过 network 层全局带宽统计）。
local network = require("network")
local robot = require("robot")
local utils = require("utils")
local log = require("log")

local udp_seq = 0

-- 构造 UDP 心跳 body（39 字节）
local function build_udp_heart()
    local battleId = robot.get("battleId") or 0
    local fighterIndex = robot.get("fighterIndex") or 0
    local session = robot.get("battleSession") or 0
    local ack = robot.get("battleAck") or 0

    local idx = robot.increment("packageIndex") % 65536

    udp_seq = udp_seq + 1
    if udp_seq > 4294967295 then udp_seq = 1 end

    local rtt = utils.random_int(10, 40)
    local now_ms = utils.time_ms()

    local p = utils.pack_le("u16", idx)
    p = p .. utils.pack_le("i64", battleId)
    p = p .. utils.pack_le("u8", fighterIndex)
    p = p .. utils.pack_le("i64", session)
    p = p .. utils.pack_le("i32", ack)
    p = p .. utils.pack_le("u16", rtt)
    p = p .. utils.pack_le("u64", now_ms)
    p = p .. utils.pack_le("u32", udp_seq)
    p = p .. utils.pack_le("u16", 0) -- LossCount
    p = p .. utils.pack_le("u8", 0)  -- Fps
    p = p .. utils.pack_le("u8", 0)  -- TargetFps
    return p
end

function execute(r)
    local battleAddress = robot.get("battleAddress")
    if not battleAddress or battleAddress == "" then
        log.error("ConnectBattleUDP: 无战斗服地址")
        return 41, 0, 0  -- 41=ADDR_EMPTY
    end

    local ok = network.connect_udp("battle", battleAddress)
    if not ok then
        log.error("ConnectBattleUDP 连接失败: " .. battleAddress)
        return 1, 0, 0  -- 1=CONN_NOT_FOUND
    end

    -- 设置 UDP 密钥（从 listen_start_loading 保存）
    local key = robot.get("battleSecretKey")
    if key then
        network.set_udp_secret_key("battle", key)
    end

    -- 新一轮战斗开始：复位包序号（对齐旧工具 ClearBattleInfo → packetIndex=0）
    udp_seq = 0
    robot.set("packageIndex", 0)
    robot.set("battleAck", 0)
    robot.set("frameCount", 0)

    -- 注册 150ms UDP 心跳
    local hbCode = network.register_udp_heartbeat("battle", 150, {cmd=4, act=2}, build_udp_heart)
    if hbCode ~= 0 then
        return hbCode, 0, 0
    end

    log.info("战斗服 UDP 连接完成 心跳已注册(150ms)")
    return 0, 0, 0
end
