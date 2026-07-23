-- listen_ob_frame.lua: 观战帧推送(32|1 NothotobPutRoomFrameS2C)解析回调
-- listens.obFrame 配了 s2cProto，runtime 解码后 on_message 收到字段表 { frames = {…} }；
-- frames 是一包连续的战斗 lockstep 帧。观战为纯下行、无需回 ack（服务器单边维护下发游标，
-- 已由服务器代码 relay.go 确认——与战斗 4|11 双向带 Ack 本质不同），此处对标
-- listen_frame_data.lua 解析 battleAck：统计每批帧数与累计帧数，供观战吞吐监控。
-- 回调在 Robot 队列串行执行，get/set 读改写无竞态。
local robot = require("robot")

function on_message(r, msg)
    robot.increment("obFrameBatches")
    -- s2cProto 已配时 msg 为字段表；防御性兼容 frames / Frames 两种命名与异常空表
    if type(msg) ~= "table" then
        return
    end
    local frames = msg.frames or msg.Frames
    local n = (type(frames) == "table") and #frames or 0
    if n > 0 then
        local total = robot.get("obFramesSeen") or 0
        robot.set("obFramesSeen", total + n)
    end
end
