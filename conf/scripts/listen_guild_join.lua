-- listen_guild_join.lua: 处理加入社团推送（21:15）
-- 只有推送里携带 data 时才说明当前玩家已经成为社团成员。
local robot = require("robot")
local log = require("log")

local function to_number(v)
    if v == nil then return nil end
    if type(v) == "number" then return v end
    return tonumber(v)
end

function on_message(r, msg)
    if not msg then return end

    local ok, err = pcall(function()
        if type(msg) ~= "table" then return end

        local roleId = to_number(robot.get("roleId"))
        local memberId = to_number(msg.memberId)
        if roleId ~= nil and memberId == roleId and type(msg.data) == "table" then
            robot.set_path("playerData.guildInfo", msg.data)
            log.info("收到加入社团成功推送: roleId=" .. tostring(roleId)
                .. " guildId=" .. tostring(msg.data.guildId))
            return
        end

        log.debug("收到社团成员加入推送，非当前玩家入会: roleId=" .. tostring(roleId)
            .. " memberId=" .. tostring(memberId)
            .. " guildId=" .. tostring(msg.guildId))
    end)
    if not ok then
        log.warn("社团加入推送解析失败: " .. tostring(err))
    end
end
