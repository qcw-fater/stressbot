-- listen_guild_join.lua: 处理加入社团推送（21:15）
-- 只有推送里携带 data 时才说明当前玩家已经成为社团成员。
local robot = require("robot")
local proto = require("proto")
local log = require("log")

local function to_number(v)
    if v == nil then return nil end
    if type(v) == "number" then return v end
    return tonumber(v)
end

function onMessage(r, msg)
    if not msg then return end

    local ok, err = pcall(function()
        local fm = proto.get_field_map(msg)
        if not fm then return end

        if type(fm.data) == "table" then
            robot.set_path("playerData.guildInfo", fm.data)
            log.info("收到加入社团成功推送: roleId=" .. tostring(robot.get("roleId"))
                .. " guildId=" .. tostring(fm.data.guildId))
            return
        end

        log.debug("收到社团成员加入推送，非当前玩家入会: roleId=" .. tostring(robot.get("roleId"))
            .. " memberId=" .. tostring(fm.memberId)
            .. " guildId=" .. tostring(fm.guildId))
    end)
    if not ok then
        log.warn("社团加入推送解析失败: " .. tostring(err))
    end
end
