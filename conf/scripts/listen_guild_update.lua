-- listen_guild_update.lua: 处理社团信息更新推送（21:13）
-- 对齐旧工具 UpdateGuildInfo：更新社团详情 currentGuildInfo，不覆盖当前玩家成员身份。
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function onMessage(r, msg)
    if not msg then return end

    local ok, err = pcall(function()
        local fm = proto.get_field_map(msg)
        if not fm then return end

        if fm.baseInfo ~= nil then
            robot.set_path("currentGuildInfo.baseInfo", fm.baseInfo)
            robot.set_path("playerData.guildInfo.baseInfo", fm.baseInfo)
            if fm.baseInfo.uid ~= nil then
                robot.set_path("playerData.guildInfo.guildId", fm.baseInfo.uid)
            end
        end
        if fm.baseSetting ~= nil then
            robot.set_path("currentGuildInfo.baseSetting", fm.baseSetting)
            robot.set_path("playerData.guildInfo.baseSetting", fm.baseSetting)
        end
        if fm.statistics ~= nil then
            robot.set_path("currentGuildInfo.statistics", fm.statistics)
        end
        if fm.timeRecord ~= nil then
            robot.set_path("currentGuildInfo.timeRecord", fm.timeRecord)
        end
        if fm.routData ~= nil then
            robot.set_path("currentGuildInfo.route", fm.routData)
            robot.set_path("playerData.guildInfo.routeData", fm.routData)
        end

        log.debug("收到社团信息更新推送: roleId=" .. tostring(robot.get("roleId")))
    end)
    if not ok then
        log.warn("社团信息更新推送解析失败: " .. tostring(err))
    end
end
