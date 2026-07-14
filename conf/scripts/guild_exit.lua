-- guild_exit.lua: 安全退出社团；会长不能直接退出，只有服务端确认成功后才清理本地状态
local robot = require("robot")
local network = require("network")
local proto = require("proto")
local log = require("log")

local function to_number(v)
    if v == nil then return nil end
    if type(v) == "number" then return v end
    return tonumber(v)
end

local function clear_guild_state(roleId)
    robot.set_path("playerData.guildInfo", { guildId = 0 })
    robot.set("currentGuildInfo", nil)
    robot.set("guildMembers", nil)
    robot.set("guildApplyList", nil)
    robot.delete("taskGuildCreatedId")
    log.info("ExitGuild 成功，清理本地社团状态: roleId=" .. tostring(roleId))
end

function execute(r)
    local roleId = to_number(robot.get("roleId"))
    local guildId = robot.get_path("playerData.guildInfo.guildId")
    local position = to_number(robot.get_path("playerData.guildInfo.mydata.position"))

    if guildId == nil or tostring(guildId) == "0" then
        log.info("ExitGuild 跳过: 本地不在社团 roleId=" .. tostring(roleId))
        return nil
    end

    if position ~= nil and position == 0 then
        log.info("ExitGuild 跳过: 当前是会长，需先传位 roleId=" .. tostring(roleId)
            .. " guildId=" .. tostring(guildId))
        return nil
    end

    local msg = proto.create("Game.GuildExitC2S")
    local err = network.tcp_request("logic", {cmd=21, act=6}, msg, "Game.GuildExitS2C")
    if err then
        log.error("ExitGuild 失败: roleId=" .. tostring(roleId)
            .. " guildId=" .. tostring(guildId)
            .. " position=" .. tostring(position)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    clear_guild_state(roleId)
    return nil
end
