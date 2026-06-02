-- listen_frame_data.lua: UDP 帧同步回调（CMD=4, ACT=11）
-- 对齐旧工具 Robot/agent/listen.go:ListenFrameData
-- 解析 UDP 原始二进制帧数据，提取帧序号（byte[12..15] 小端 uint32）并写入 battleAck。
-- 高频回调正常路径不打日志；异常只按次数限频记录，避免压测时刷屏。
--
-- 参数：
--   r   - robot 对象（未使用，签名对齐回调约定）
--   msg - 原始二进制字节字符串（无 proto 时 runtime 以字符串传入）
local robot = require("robot")
local log = require("log")

local function should_log_invalid(count)
    return count == 1 or count == 10 or count == 100
end

local function record_invalid(reason, msg)
    local count = robot.increment("frameDataInvalidCount")
    local len = 0
    if type(msg) == "string" then
        len = #msg
    end
    if should_log_invalid(count) then
        log.warn("帧同步回调收到无效数据: count=" .. tostring(count)
            .. " reason=" .. tostring(reason)
            .. " type=" .. tostring(type(msg))
            .. " len=" .. tostring(len)
            .. " battleId=" .. tostring(robot.get("battleId"))
            .. " fighterIndex=" .. tostring(robot.get("fighterIndex")))
    end
end

function onMessage(r, msg)
    if msg == nil or type(msg) ~= "string" then
        record_invalid("type", msg)
        return
    end
    if #msg < 16 then
        record_invalid("short", msg)
        return
    end

    -- 小端 uint32：byte[13], byte[14], byte[15], byte[16]（Lua 1-based，对应旧工具 data[12:16]）
    local b1 = string.byte(msg, 13)
    local b2 = string.byte(msg, 14)
    local b3 = string.byte(msg, 15)
    local b4 = string.byte(msg, 16)
    if not (b1 and b2 and b3 and b4) then
        record_invalid("bytes", msg)
        return
    end

    local frameIndex = b1 + b2 * 256 + b3 * 65536 + b4 * 16777216
    robot.set("battleAck", frameIndex)
end
