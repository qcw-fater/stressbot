-- guild_appoint_member.lua: 安全任命普通成员为小队长；没有候选时跳过
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

local function member_player_id(m)
    if type(m) ~= "table" then return nil end
    if type(m.baseData) == "table" then
        return to_number(m.baseData.playerId)
    end
    if type(m.guildData) == "table" then
        return to_number(m.guildData.playerId)
    end
    return nil
end

local function member_position(m)
    if type(m) ~= "table" or type(m.guildData) ~= "table" then return nil end
    return to_number(m.guildData.position)
end

function execute(r)
    local roleId = to_number(robot.get("roleId"))
    local members = robot.get("guildMembers")
    local candidates = {}

    if type(members) == "table" then
        for _, m in ipairs(members) do
            local pid = member_player_id(m)
            local pos = member_position(m)
            if pid ~= nil and pid ~= roleId and pos ~= nil and pos == 2 then
                table.insert(candidates, pid)
            end
        end
    end

    if #candidates == 0 then
        log.info("GuildAppointMember 无普通成员候选，跳过: roleId=" .. tostring(roleId))
        return 0
    end

    local target = utils.random_pick(candidates)
    local msg = proto.create("Game.GuildAppointC2S")
    proto.set_field(msg, "playerId", target)
    proto.set_field(msg, "position", 1)

    local code = network.tcp_request("logic", {cmd=21, act=11}, msg, "Game.GuildAppointS2C")
    if code ~= 0 then
        log.error("GuildAppointMember 失败: roleId=" .. tostring(roleId)
            .. " target=" .. tostring(target)
            .. " code=" .. tostring(code))
        return code
    end

    log.info("GuildAppointMember 成功: roleId=" .. tostring(roleId)
        .. " target=" .. tostring(target))
    return 0
end
