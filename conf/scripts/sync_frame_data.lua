-- sync_frame_data.lua: UDP 帧同步（CMD=4, ACT=11）
-- 与旧工具一致：发送自定义二进制帧数据
-- 使用 utils.pack_le 处理大整数（snowflake ID）的二进制打包
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

function execute(r)
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

    -- 构建帧同步二进制数据（little-endian）
    local frameData = utils.pack_le("u16", packageIndex)       -- PacketIndex
        .. utils.pack_le("i64", battleId)                       -- BattleId
        .. utils.pack_le("u8", fighterIndex)                    -- FighterIndex
        .. utils.pack_le("i64", session)                        -- Session
        .. utils.pack_le("u64", utils.time_ms())                -- MsUnixTime
        .. utils.pack_le("i32", robot.get("battleAck") or 0)    -- Ack（由 listen_frame_data 更新）
        .. utils.pack_le("i32", packageIndex)                   -- Index
        .. utils.pack_le("u8", 4)                               -- Cmd = BASE_ATTACK
        .. string.char(1, 2, 3, 4, 5, 6)                       -- dummy data (6 bytes)

    -- 通过 UDP 发送（带协议头 CMD=4, ACT=11）
    local code = network.udp_send_msg(4, 11, frameData)

    -- 每 20 帧打一次日志：真实上限由 conf/flow.json 的 syncLoop.loopCount 控制，
    -- 这里只显示本轮实际已发送帧数（frameCount）与共享包序号（pkgIdx）。
    if frameCount % 20 == 0 then
        utils.log_info("SyncFrame: frame=" .. frameCount .. " (pkgIdx=" .. packageIndex .. ")")
    end

    -- 60ms 间隔（约 16fps）
    utils.sleep(60)

    return 0
end
