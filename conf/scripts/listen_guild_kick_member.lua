-- listen_guild_kick_member.lua: 处理被踢/退出成员推送（21:16）
-- 对齐旧工具 ListenGuildKickMember：只要推送里的 playerId 是自己，就清空本地社团状态。
local robot = require("robot")
local proto = require("proto")
local log = require("log")

local function to_number(v)
    if v == nil then return nil end
    if type(v) == "number" then return v end
    return tonumber(v)
end

local function clear_guild_state(roleId)
    robot.set_path("playerData.guildInfo.guildId", 0)
    robot.set_path("playerData.guildInfo.guildGamePlay", nil)
    robot.set_path("playerData.guildInfo.baseInfo", nil)
    robot.set_path("playerData.guildInfo.baseSetting", nil)
    robot.set_path("playerData.guildInfo.mydata", nil)
    robot.set_path("playerData.guildInfo.routeData", nil)
    robot.set("currentGuildInfo", nil)
    robot.set("guildMembers", nil)
    robot.set("guildApplyList", nil)
    log.info("收到自己离开/被移出社团推送，清理本地社团状态: roleId=" .. tostring(roleId))
end

function onMessage(r, msg)
    if not msg then return end

    local ok, err = pcall(function()
        local fm = proto.get_field_map(msg)
        if not fm then return end

        local roleId = to_number(robot.get("roleId"))
        local playerId = to_number(fm.playerId)
        if roleId ~= nil and playerId == roleId then
            clear_guild_state(roleId)
        end
    end)
    if not ok then
        log.warn("社团移除成员推送解析失败: " .. tostring(err))
    end
end
