-- match_succeed.lua: 等待当前轮匹配成功（tcp_listen CMD=3, ACT=1）
-- 持久监听队列可能残留上一轮推送，以当前 teamId 作硬关联；playerStatus 只用于诊断，
-- 因为状态推送虽由服务端先发送，本地异步监听状态仍可能晚于 MatchSucceed 可见。
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")
local log = require("log")

local MATCH_TIMEOUT_MS = 1860 * 1000

local function same_id(left, right)
    if left == nil or right == nil then
        return false
    end
    return tostring(left) == tostring(right)
end

local function valid_id(value)
    return value ~= nil and value ~= 0 and value ~= "0"
end

local function remaining_seconds(deadlineMs)
    local remainingMs = deadlineMs - utils.time_ms()
    if remainingMs <= 0 then
        return nil
    end
    return math.max(1, math.ceil(remainingMs / 1000))
end

local function parse_candidate(resp, roleId)
    local candidate = {
        actorCount = proto.list_size(resp, "actorList"),
        battleArea = proto.get_path(resp, "battleArea"),
    }
    local myPlayerId = tonumber(roleId)
    if candidate.actorCount > 0 and myPlayerId then
        for i = 1, candidate.actorCount do
            local actor = proto.list_get(resp, "actorList", i)
            local playerId = tonumber(proto.get_path(actor, "playerId"))
            if playerId and playerId == myPlayerId then
                candidate.actorTeamId = proto.get_path(actor, "matchData.teamId")
                candidate.battleSession = proto.get_path(actor, "matchData.sessionId")
                break
            end
        end
    end
    return candidate
end

-- 当前轮尚未发送确认，3:5/2:10 队列中已有的消息只能属于旧轮。
-- 在 ConfirmMatchSuccess 前清空，避免持久监听把上一轮终态交给下一轮消费。
local function drain_route(route, label)
    local count = 0
    while true do
        local err, raw = network.try_tcp_listen("logic", route)
        if err then
            if tonumber(err.code) ~= 31 then
                log.warn("清理旧 " .. label .. " 失败: code=" .. tostring(err.code)
                    .. " detail=" .. tostring(err.detail))
            end
            break
        end
        if raw == nil or raw == "" then
            break
        end
        count = count + 1
    end
    if count > 0 then
        log.warn("确认匹配前清理旧 " .. label .. ": count=" .. tostring(count))
    end
end

function execute(r)
    local roleId = robot.get("roleId")
    local currentTeamId = robot.get("teamId")
    if not valid_id(currentTeamId) then
        return robot.error(54, "MatchSucceed 缺少当前 teamId: roleId=" .. tostring(roleId))
    end

    -- 所有旧消息共用一个总等待窗口，不能每丢弃一条就重置 1860 秒。
    local deadlineMs = utils.time_ms() + MATCH_TIMEOUT_MS
    while true do
        local timeoutSec = remaining_seconds(deadlineMs)
        if timeoutSec == nil then
            return robot.error(31, "等待当前轮 MatchSucceed 超时: roleId=" .. tostring(roleId)
                .. " teamId=" .. tostring(currentTeamId))
        end

        local err, resp = network.tcp_listen(
            "logic", {cmd=3, act=1}, "Game.MatchSucceedS2C", timeoutSec)
        if err then
            log.error("匹配成功消息等待失败: service=logic route=3:1 proto=Game.MatchSucceedS2C roleId="
                .. tostring(roleId) .. " teamId=" .. tostring(currentTeamId)
                .. " remainingSec=" .. tostring(timeoutSec)
                .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
            return err
        end

        local ok, candidate = pcall(parse_candidate, resp, roleId)
        if not ok then
            log.error("MatchSucceed 解析失败: roleId=" .. tostring(roleId)
                .. " teamId=" .. tostring(currentTeamId)
                .. " err=" .. tostring(candidate))
            return robot.error(12, "MatchSucceed 解析失败: roleId=" .. tostring(roleId)
                .. " teamId=" .. tostring(currentTeamId)
                .. " err=" .. tostring(candidate))
        end

        local teamMatches = same_id(candidate.actorTeamId, currentTeamId)
        local playerStatus = tonumber(robot.get("playerStatus"))
        if teamMatches then
            if not valid_id(candidate.battleSession) then
                return robot.error(54, "当前轮 MatchSucceed 缺少 battleSession: roleId="
                    .. tostring(roleId) .. " teamId=" .. tostring(currentTeamId)
                    .. " actorCount=" .. tostring(candidate.actorCount))
            end

            drain_route({cmd=3, act=5}, "MatchEnterRoom")
            drain_route({cmd=2, act=10}, "MainBackLobby")
            robot.delete("matchRoomId")
            robot.set("battleSession", candidate.battleSession)
            if candidate.battleArea ~= nil then
                robot.set("battleArea", candidate.battleArea)
            end
            log.info("匹配成功: roleId=" .. tostring(roleId)
                .. " teamId=" .. tostring(currentTeamId)
                .. " actorCount=" .. tostring(candidate.actorCount)
                .. " battleSession=" .. tostring(candidate.battleSession)
                .. " battleArea=" .. tostring(candidate.battleArea)
                .. " playerStatus=" .. tostring(playerStatus))
            return nil
        end

        log.warn("丢弃旧 MatchSucceed: roleId=" .. tostring(roleId)
            .. " currentTeamId=" .. tostring(currentTeamId)
            .. " actorTeamId=" .. tostring(candidate.actorTeamId)
            .. " candidateSession=" .. tostring(candidate.battleSession)
            .. " playerStatus=" .. tostring(playerStatus)
            .. " actorCount=" .. tostring(candidate.actorCount))
    end
end
