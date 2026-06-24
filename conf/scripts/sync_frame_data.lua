-- sync_frame_data.lua: UDP 帧同步（CMD=4, ACT=11）
-- 与旧工具一致：发送自定义二进制帧数据
-- 使用 utils.pack_le 处理大整数（snowflake ID）的二进制打包
local network = require("network")
local robot = require("robot")
local utils = require("utils")
local log = require("log")

function execute(r)
    -- 非阻塞消费最新服务端 ack 帧（CMD=4, ACT=11），更新 battleAck（主流程 pull 模型，取代已下线的 listen 脚本回调）。
    -- queueSize=1 保证取到的是最新；无 ack 则 battleAck 保持上轮值（松散「保最新」语义）。
    -- 解析 byte[13..16] 小端 uint32（对齐旧工具帧序号解析）。
    local ackErr, ackData = network.try_udp_listen("battle", {cmd=4, act=11})
    if not ackErr and type(ackData) == "string" and #ackData >= 16 then
        local b1, b2, b3, b4 = string.byte(ackData, 13, 16)
        if b1 and b2 and b3 and b4 then
            robot.set("battleAck", b1 + b2 * 256 + b3 * 65536 + b4 * 16777216)
        end
    end

    -- packageIndex 由 UDP 心跳、UDP 帧同步、TCP 战斗心跳共享（对齐旧工具 r.battle.packetIndex）
    -- 每帧 +1，uint16 回绕（与旧工具 r.battle.packetIndex++ 一致）
    local packageIndex = ((robot.get("packageIndex") or 0) + 1) % 65536
    robot.set("packageIndex", packageIndex)

    -- frameCount 仅用于本轮 syncLoop 内部计数（与 packageIndex 解耦），方便观测
    local frameCount = (robot.get("frameCount") or 0) + 1
    robot.set("frameCount", frameCount)

    local battleId = robot.get("battleId") or 0
    local fighterIndex = robot.get("fighterIndex") or 0
    local session = robot.get("battleSession") or 0
    local battleAck = robot.get("battleAck") or 0

    -- 构建帧同步二进制数据（little-endian）
    local frameData = utils.pack_le("u16", packageIndex)       -- PacketIndex
        .. utils.pack_le("i64", battleId)                       -- BattleId
        .. utils.pack_le("u8", fighterIndex)                    -- FighterIndex
        .. utils.pack_le("i64", session)                        -- Session
        .. utils.pack_le("u64", utils.time_ms())                -- MsUnixTime
        .. utils.pack_le("i32", battleAck)                      -- Ack（由本轮开头 try_udp_listen 更新）
        .. utils.pack_le("i32", packageIndex)                   -- Index
        .. utils.pack_le("u8", 4)                               -- Cmd = BASE_ATTACK
        .. string.char(1, 2, 3, 4, 5, 6)                       -- dummy data (6 bytes)

    -- 通过 UDP 发送（带协议头 CMD=4, ACT=11）
    local err = network.udp_send("battle", {cmd=4, act=11}, frameData)
    if err then
        log.warn("SyncFrame 发送失败: frame=" .. tostring(frameCount)
            .. " packageIndex=" .. tostring(packageIndex)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " battleAck=" .. tostring(battleAck)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    -- 每 20 帧打一次 debug 日志：真实上限由 conf/flow.json 的 syncLoop.loopCount 控制。
    if frameCount % 20 == 0 then
        log.debug("SyncFrame: frame=" .. tostring(frameCount)
            .. " packageIndex=" .. tostring(packageIndex)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " battleAck=" .. tostring(battleAck))
    end

    -- 60ms 间隔（约 16fps）
    utils.sleep(60)

    return nil
end
