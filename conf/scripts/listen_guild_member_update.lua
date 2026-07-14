-- listen_guild_member_update.lua: 处理当前玩家社团成员数据更新（21:14）
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
        if type(msg) ~= "table" or type(msg.member) ~= "table"
            or type(msg.member.guildData) ~= "table" then
            return
        end

        local roleId = to_number(robot.get("roleId"))
        local guildData = msg.member.guildData
        local playerId = to_number(guildData.playerId)
        if roleId == nil or playerId ~= roleId then
            return
        end

        local guildId = msg.uid or guildData.guildUid
        if guildId == nil or tostring(guildId) == "0" then
            robot.set_path("playerData.guildInfo.guildId", 0)
            robot.set_path("playerData.guildInfo.mydata.guildUid", 0)
            robot.set("guildMembers", nil)
            robot.set("guildApplyList", nil)
            log.info("收到社团成员清空更新，清理本地社团状态: roleId=" .. tostring(roleId))
            return
        end

        robot.set_path("playerData.guildInfo.guildId", guildId)
        robot.set_path("playerData.guildInfo.mydata", guildData)
        if msg.guildName ~= nil then
            robot.set_path("playerData.guildInfo.baseInfo.name", msg.guildName)
        end
        log.debug("收到社团成员数据更新: roleId=" .. tostring(roleId)
            .. " guildId=" .. tostring(guildId)
            .. " position=" .. tostring(guildData.position))
    end)
    if not ok then
        log.warn("社团成员更新推送解析失败: " .. tostring(err))
    end
end
