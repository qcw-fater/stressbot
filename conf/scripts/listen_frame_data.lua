-- listen_frame_data.lua: UDP 帧同步回调（CMD=4, ACT=11）
-- 对齐旧工具 Robot/agent/listen.go:ListenFrameData
-- 解析 UDP 原始二进制帧数据，提取帧序号（byte[12..15] 小端 uint32）并写入 battleAck。
--
-- 参数：
--   r   - robot 对象（未使用，签名对齐回调约定）
--   msg - 原始二进制字节字符串（无 proto 时 runtime 以字符串传入）
local robot = require("robot")

function onMessage(r, msg)
    if msg == nil or type(msg) ~= "string" or #msg < 16 then
        return
    end

    -- 小端 uint32：byte[13], byte[14], byte[15], byte[16]（Lua 1-based，对应旧工具 data[12:16]）
    local b1 = string.byte(msg, 13)
    local b2 = string.byte(msg, 14)
    local b3 = string.byte(msg, 15)
    local b4 = string.byte(msg, 16)
    if not (b1 and b2 and b3 and b4) then
        return
    end

    local frameIndex = b1 + b2 * 256 + b3 * 65536 + b4 * 16777216
    robot.set("battleAck", frameIndex)
end
