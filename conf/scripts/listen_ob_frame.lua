-- listen_ob_frame.lua: 观战帧推送(32|1 NothotobPutRoomFrame)计数回调
-- observer 高频扇出帧批次；此处只累加批次计数（不解析帧内容，热路径尽量轻），
-- 收发字节由 network 层全局统计。配合 monitor 的 callback 计时可得观战帧吞吐。
-- 无 s2cProto（脚本型 listen），msg 为 nil。
local robot = require("robot")

function on_message(r, msg)
    robot.increment("obFrameBatches")
end
