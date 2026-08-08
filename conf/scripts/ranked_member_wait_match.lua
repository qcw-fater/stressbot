-- ranked_member_wait_match.lua: 队员轮询 Redis 队伍 status 等队长开匹配
-- matching → 置 rankedMatchStarted；failed/done/超时 → 置 rankedRoundFailed。返回 nil（rankedAfterMatchStart 把关）。
local share = require("share")
local robot = require("robot")
local utils = require("utils")
local log = require("log")

local TEAM_PREFIX = "ranked:v2:team:"
local WAIT_MS = 20000
local POLL_MS = 500

function execute(r)
    robot.delete("rankedMatchStarted")
    robot.delete("rankedRoundFailed")
    local tk = robot.get("rankedTeamKey")
    if not tk then
        robot.set("rankedRoundFailed", "member_match_no_team_key")
        log.warn("排位队员等匹配: teamKey 为空，跳过")
        return nil
    end
    local deadline = utils.time_ms() + WAIT_MS
    while utils.time_ms() < deadline do
        local status = share.hash_get(TEAM_PREFIX .. tostring(tk), "status")
        if status == "matching" then
            robot.set("rankedMatchStarted", true)
            log.info("排位队员等匹配: 已开始")
            return nil
        end
        if status == "failed" or status == "done" then
            robot.set("rankedRoundFailed", "member_match_team_" .. tostring(status))
            log.warn("排位队员等匹配: 队伍已结束 status=" .. tostring(status) .. "，跳过")
            return nil
        end
        utils.sleep(POLL_MS)
    end
    robot.set("rankedRoundFailed", "member_match_timeout")
    log.warn("排位队员等匹配: 超时，跳过")
    return nil
end
