-- guild_audit_join.lua: 安全审核社团申请；没有申请时跳过
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

function execute(r)
    local roleId = to_number(robot.get("roleId"))
    local requests = robot.get("guildApplyList")
    local candidates = {}

    if type(requests) == "table" then
        for _, item in ipairs(requests) do
            if type(item) == "table" and type(item.request) == "table" then
                local pid = to_number(item.request.playerId)
                if pid ~= nil then
                    table.insert(candidates, pid)
                end
            end
        end
    end

    if #candidates == 0 then
        log.info("AuditGuildJoin 无申请候选，跳过: roleId=" .. tostring(roleId))
        return nil
    end

    local target = utils.random_pick(candidates)
    local operate = utils.random_int(2) -- 0 拒绝 / 1 通过
    local msg = proto.create("Game.GuildAuditC2S")
    proto.set_field(msg, "joinPlayerId", target)
    proto.set_field(msg, "operate", operate)

    local err = network.tcp_request("logic", {cmd=21, act=7}, msg, "Game.GuildAuditS2C")
    if err then
        log.error("AuditGuildJoin 失败: roleId=" .. tostring(roleId)
            .. " target=" .. tostring(target)
            .. " operate=" .. tostring(operate)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    log.info("AuditGuildJoin 成功: roleId=" .. tostring(roleId)
        .. " target=" .. tostring(target)
        .. " operate=" .. tostring(operate))
    return nil
end
