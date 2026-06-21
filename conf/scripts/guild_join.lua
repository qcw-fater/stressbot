-- guild_join.lua: 申请/加入社团
-- GuildJoinS2C 在待审核时也会返回 uid，但没有 mydata；这种状态不能写入 playerData.guildInfo，
-- 否则本地会误以为已入会，或者反复对已在社团的玩家走 outGuild 分支。
local robot = require("robot")
local network = require("network")
local proto = require("proto")
local log = require("log")
local utils = require("utils")

local function to_number(v)
    if v == nil then return nil end
    if type(v) == "number" then return v end
    return tonumber(v)
end

local function current_guild_id()
    return to_number(robot.get_path("playerData.guildInfo.guildId"))
end

local function pick_guild_id(list)
    local candidates = {}
    if type(list) ~= "table" then return nil end
    for _, item in ipairs(list) do
        if type(item) == "table" and type(item.baseInfo) == "table" then
            local uid = to_number(item.baseInfo.uid)
            if uid ~= nil and uid ~= 0 then
                table.insert(candidates, uid)
            end
        end
    end
    if #candidates == 0 then return nil end
    return utils.random_pick(candidates)
end

function execute(r)
    local roleId = to_number(robot.get("roleId"))
    local beforeGuildId = current_guild_id()
    if beforeGuildId ~= nil and beforeGuildId ~= 0 then
        log.debug("JoinGuild 跳过: 本地已在社团 roleId=" .. tostring(roleId)
            .. " guildId=" .. tostring(beforeGuildId))
        return 0
    end

    local target = pick_guild_id(robot.get("guildList"))
    if target == nil then
        log.info("JoinGuild 无可申请社团，跳过: roleId=" .. tostring(roleId))
        return 0
    end

    local msg = proto.create("Game.GuildJoinC2S")
    proto.set_field(msg, "uid", target)
    proto.set_field(msg, "msg", "申请加入社团")
    proto.set_field(msg, "account", tostring(robot.get("account")))

    local code, resp = network.tcp_request("logic", {cmd=21, act=3}, msg, "Game.GuildJoinS2C")
    if code ~= 0 then
        log.error("JoinGuild 失败: roleId=" .. tostring(roleId)
            .. " target=" .. tostring(target)
            .. " code=" .. tostring(code))
        return code
    end

    local fm = proto.get_field_map(resp)
    robot.set("lastGuildJoinResp", fm)

    if type(fm) == "table" and type(fm.mydata) == "table" then
        robot.set_path("playerData.guildInfo.guildId", fm.uid)
        robot.set_path("playerData.guildInfo.baseInfo", fm.baseInfo)
        robot.set_path("playerData.guildInfo.mydata", fm.mydata)
        log.info("JoinGuild 成功加入社团: roleId=" .. tostring(roleId)
            .. " guildId=" .. tostring(fm.uid))
    else
        log.info("JoinGuild 已提交申请，等待审核: roleId=" .. tostring(roleId)
            .. " guildId=" .. tostring(target))
    end
    return 0
end
