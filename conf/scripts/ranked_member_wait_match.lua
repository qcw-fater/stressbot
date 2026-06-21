-- ranked_member_wait_match.lua: 队员等待队长开始匹配
-- 仅队员角色执行。轮询 Redis 队伍状态直到 matching。
-- 成功后标记 rankedMatchStarted，超时时标记本轮失败，避免继续进入 MatchSucceed/Confirm/Loading。
local share = require("share")
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local utils = require("utils")
local log = require("log")

local TEAM_TTL = 600
local TEAM_PREFIX = "ranked:v2:team:"
local TIMEOUT_MS = 12000  -- Redis 协调只短等，避免影响长期压测吞吐

local function currentTeamId()
    local tid = robot.get("teamId")
    if tid then
        return tid
    end
    local teamData = robot.get("teamData")
    if type(teamData) == "table" then
        if teamData.id then
            return teamData.id
        end
        if type(teamData.teamData) == "table" and teamData.teamData.id then
            return teamData.teamData.id
        end
    end
    return nil
end

local function markRoundFailed(reason)
    robot.delete("rankedMatchStarted")
    robot.set("rankedRoundFailed", tostring(reason))

    local teamKey = robot.get("rankedTeamKey")
    if teamKey then
        local hashKey = TEAM_PREFIX .. tostring(teamKey)
        share.hash_set(hashKey, "status", "failed", TEAM_TTL)
        share.hash_set(hashKey, "failReason", tostring(reason), TEAM_TTL)
    end

    local tid = currentTeamId()
    local msg = proto.create("Game.TeamLeaveC2S")
    if tid then
        proto.set_field(msg, "teamId", tonumber(tid))
    end
    local code = network.tcp_request("logic", {cmd=5, act=2}, msg, "Game.TeamLeaveS2C")
    log.info("排位队员等匹配清理退出队伍: reason=" .. tostring(reason)
        .. " teamId=" .. tostring(tid) .. " code=" .. tostring(code))
    robot.delete("teamId")
end

function execute(r)
    robot.delete("rankedMatchStarted")
    robot.delete("rankedRoundFailed")

    local teamKey = robot.get("rankedTeamKey")
    if not teamKey then
        log.warn("排位队员等匹配: teamKey 为空，本轮跳过匹配")
        markRoundFailed("member_match_no_team_key")
        return 0
    end

    local hashKey = TEAM_PREFIX .. teamKey
    local waited = 0
    while waited < TIMEOUT_MS do
        local status, _, _ = share.hash_get(hashKey, "status")
        if status == "matching" then
            robot.set("rankedMatchStarted", true)
            robot.delete("rankedRoundFailed")
            log.info("排位队员等匹配: 匹配已开始")
            return 0
        end
        if status == "failed" or status == "done" then
            log.warn("排位队员等匹配: 队伍已结束 status=" .. tostring(status) .. "，本轮跳过匹配")
            markRoundFailed("member_match_team_" .. tostring(status))
            return 0
        end
        utils.sleep(500)
        waited = waited + 500
    end

    log.warn("排位队员等匹配: 超时，本轮跳过匹配")
    markRoundFailed("member_match_timeout")
    return 0
end
