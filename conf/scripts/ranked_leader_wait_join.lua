-- ranked_leader_wait_join.lua: 队长轮询 Redis :members 等已邀请队员 joined
-- joined(含 ready) 数达标即继续；超时仍 0 入队 → 置 Redis failed + 返回 err(skip)，不降级单排。
local share = require("share")
local robot = require("robot")
local utils = require("utils")
local log = require("log")

local TEAM_TTL = 600
local WAIT_MS = 15000
local POLL_MS = 250

local function countJoined(tk)
    local members, err = share.hash_get_all("ranked:v2:team:" .. tostring(tk) .. ":members")
    if err or not members then return 0 end
    local n = 0
    for _, m in pairs(members) do
        if type(m) == "table" and (m.status == "joined" or m.status == "ready") then
            n = n + 1
        end
    end
    return n
end

function execute(r)
    local tk = robot.get("rankedTeamKey")
    if not tk then
        return robot.error(54, "RankedLeaderWaitJoin 缺少 teamKey")
    end
    local expected = tonumber(robot.get("rankedTeamSize") or 1)
    local deadline = utils.time_ms() + WAIT_MS
    while utils.time_ms() < deadline do
        if countJoined(tk) >= expected then break end
        utils.sleep(POLL_MS)
    end
    local actual = countJoined(tk)
    if actual <= 1 then
        share.hash_set("ranked:v2:team:" .. tostring(tk), "status", "failed", TEAM_TTL)
        share.hash_set("ranked:v2:team:" .. tostring(tk), "failReason", "no_member_joined", TEAM_TTL)
        log.info("排位队长等入队超时无成员，跳过本轮: teamKey=" .. tostring(tk))
        return robot.error(54, "RankedLeaderWaitJoin 无人入队")
    end
    if actual > expected then actual = expected end
    robot.set("rankedTeamSize", actual)
    share.hash_set("ranked:v2:team:" .. tostring(tk), "actualSize", tostring(actual), TEAM_TTL)
    log.info("排位队长等入队完成: teamKey=" .. tostring(tk) .. " actual=" .. tostring(actual))
    return nil
end
