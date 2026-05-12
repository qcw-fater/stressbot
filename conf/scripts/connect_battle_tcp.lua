-- connect_battle_tcp.lua: 连接战斗服 TCP + 密钥交换 + 注册 10s 心跳
local network = require("network")
local robot = require("robot")
local utils = require("utils")
local log = require("log")

-- 构造 Battle TCP 心跳 body（19 字节）：
--   PacketIndex u16 | BattleId i64 | FighterIndex u8 | Session i64
local function build_battle_tcp_heart()
    local battleId = robot.get("battleId") or 0
    local fighterIndex = robot.get("fighterIndex") or 0
    local session = robot.get("battleSession") or 0

    -- PacketIndex 自增
    local idx = robot.increment("packageIndex")
    idx = idx % 65536 -- uint16 wrap

    local p = utils.pack_le("u16", idx)
    p = p .. utils.pack_le("i64", battleId)
    p = p .. utils.pack_le("u8", fighterIndex)
    p = p .. utils.pack_le("i64", session)
    return p
end

function execute(r)
    local battleAddress = robot.get("battleAddress")
    if not battleAddress or battleAddress == "" then
        log.error("ConnectBattleTCP: 无战斗服地址")
        return 1, 0, 0
    end

    log.info("连接战斗服 TCP: " .. battleAddress)

    local ok = network.connect_tcp("battle", battleAddress)
    if not ok then
        log.error("连接战斗服 TCP 失败: " .. battleAddress)
        return 1, 0, 0
    end

    -- 发送空包获取密钥并设置到连接
    local code, keyBody, sent, recv = network.tcp_request("battle")
    if code ~= 0 or not keyBody or #keyBody == 0 then
        log.error("战斗服密钥交换失败")
        return 1, sent, recv
    end
    network.set_tcp_secret_key("battle", keyBody)

    -- 注册 10 秒心跳（Battle: cmd=4 BATTLE, act=2 PING_CS）
    network.register_tcp_heartbeat("battle", 10000, {cmd=4, act=2}, build_battle_tcp_heart)

    log.info("战斗服 TCP 连接成功 心跳已注册(10s)")
    return 0, sent, recv
end
