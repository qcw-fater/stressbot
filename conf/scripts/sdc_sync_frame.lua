-- sdc_sync_frame.lua: 只负责搜打撤局内帧同步(CMD=4, ACT=11)。
-- SDC 不缓存服务端 4:11 高频帧；本流程只需发低频上行帧维持局内业务活跃。
-- 发送间隔由 flow 中的声明式 weighted + wait 节点负责；本脚本只构造并发送一帧。
local network = require("network")
local robot = require("robot")
local utils = require("utils")
local log = require("log")

function execute(r)
    -- packageIndex：每帧 +1，uint16 回绕（帧同步/TCP 战斗心跳共享）
    local packageIndex = ((robot.get("packageIndex") or 0) + 1) % 65536
    robot.set("packageIndex", packageIndex)

    local frameCount = (robot.get("frameCount") or 0) + 1
    robot.set("frameCount", frameCount)

    local battleId = robot.get("battleId") or 0
    local fighterIndex = robot.get("fighterIndex") or 0
    local session = robot.get("battleSession") or 0
    local battleAck = robot.get("battleAck") or 0

    -- 构建帧同步二进制数据（little-endian），与 sync_frame_data 一致
    local frameData = utils.pack_le("u16", packageIndex)       -- PacketIndex
        .. utils.pack_le("i64", battleId)                       -- BattleId
        .. utils.pack_le("u8", fighterIndex)                    -- FighterIndex
        .. utils.pack_le("i64", session)                        -- Session
        .. utils.pack_le("u64", utils.time_ms())                -- MsUnixTime
        .. utils.pack_le("i32", battleAck)                      -- Ack
        .. utils.pack_le("i32", packageIndex)                   -- Index
        .. utils.pack_le("u8", 4)                               -- Cmd = BASE_ATTACK
        .. string.char(1, 2, 3, 4, 5, 6)                        -- dummy data (6 bytes)

    local err = network.udp_send("sdcbattle", {cmd=4, act=11}, frameData)
    if err then
        log.warn("搜打撤 SyncFrame 发送失败: frame=" .. tostring(frameCount)
            .. " packageIndex=" .. tostring(packageIndex)
            .. " battleId=" .. tostring(battleId)
            .. " code=" .. tostring(err.code))
        return err
    end

    return nil
end
