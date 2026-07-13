-- ranked_start_match.lua: 队长/单排发 TeamStartMatch(5:20)
-- 成功 → 置 rankedMatchStarted + Redis status=matching；失败 → 置 rankedRoundFailed + Redis status=failed + 返回 err(skip)。
-- 队员经 Redis status 得知匹配结果。失败不降级单排。solo 无 teamKey，跳过 Redis 写。
local share = require("share")
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local utils = require("utils")
local log = require("log")

local TEAM_TTL = 600
local TEAM_PREFIX = "ranked:v2:team:"

function execute(r)
    robot.delete("rankedMatchStarted")
    if robot.get("rankedRoundFailed") then
        log.warn("排位开始匹配: 本轮已失败，跳过 reason=" .. tostring(robot.get("rankedRoundFailed")))
        return robot.error(54, "RankedStartMatch 本轮已失败")
    end

    local teamId = robot.get("teamId")
    local tk = robot.get("rankedTeamKey")
    local msg = proto.create("Game.TeamStartMatchC2S")
    local err, resp = network.tcp_request("logic", {cmd=5, act=20}, msg, "Game.TeamStartMatchS2C")
    if err then
        robot.set("rankedRoundFailed", "start_failed_" .. tostring(err.code))
        if tk then
            share.hash_set(TEAM_PREFIX .. tostring(tk), "status", "failed", TEAM_TTL)
            share.hash_set(TEAM_PREFIX .. tostring(tk), "failReason", "start_match_failed", TEAM_TTL)
            share.hash_set(TEAM_PREFIX .. tostring(tk), "updatedAtMs", tostring(utils.time_ms()), TEAM_TTL)
        end
        log.warn("排位开始匹配失败: code=" .. tostring(err.code) .. " teamId=" .. tostring(teamId) .. " role=" .. tostring(robot.get("rankedTeamRole")))
        return robot.error(54, "RankedStartMatch 失败 code=" .. tostring(err.code))
    end

    robot.set("rankedMatchStarted", true)
    robot.delete("rankedRoundFailed")
    if tk then
        share.hash_set(TEAM_PREFIX .. tostring(tk), "status", "matching", TEAM_TTL)
        share.hash_set(TEAM_PREFIX .. tostring(tk), "updatedAtMs", tostring(utils.time_ms()), TEAM_TTL)
    end
    local modeId = proto.get_field(resp, "modeId")
    log.info("排位开始匹配成功: teamId=" .. tostring(teamId) .. " modeId=" .. tostring(modeId))
    return nil
end
