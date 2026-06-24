local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    local ids = {3, 5, 7, 9, 11, 13}
    local failedCount = 0
    local lastErr = nil

    for _, id in ipairs(ids) do
        local msg = proto.create("Game.MainGetLevelRewardC2S")
        proto.set_field(msg, "Id", id)

        local err, data = network.tcp_request("logic", {cmd=2, act=26}, msg, "Game.MainGetLevelRewardS2C")

        if err or data == nil then
            failedCount = failedCount + 1
            if err then
                lastErr = err
            else
                lastErr = robot.error(54, "解锁基础功能响应为空: id=" .. tostring(id))  -- 54=LUA_SCRIPT_CHECK：脚本断言失败
            end
            log.error("解锁基础功能失败: service=logic route=2:26 id=" .. tostring(id)
                .. " code=" .. (err and tostring(err.code) or "nil")
                .. " detail=" .. (err and err.detail or "")
                .. " hasData=" .. tostring(data ~= nil))
        else
            log.debug("解锁基础功能成功: id=" .. tostring(id))
        end
    end

    if failedCount > 0 then
        log.warn("解锁基础功能存在失败: failed=" .. tostring(failedCount)
            .. " total=" .. tostring(#ids)
            .. " lastCode=" .. (lastErr and tostring(lastErr.code) or "nil"))
        return lastErr
    end

    return nil
end
