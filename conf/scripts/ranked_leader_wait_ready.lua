-- ranked_leader_wait_ready.lua: 队长写自己的 ready，轮询 :ready 数 >= actualSize
-- 全员 ready → 置 Redis status=ready；超时 → 置 failed + 返回 err(skip)。
local share = require("share")
local robot = require("robot")
local utils = require("utils")
local log = require("log")

local TEAM_TTL = 600
local WAIT_MS = 10000
local POLL_MS = 250

local function readyCount(tk)
    local ready, err = share.hash_get_all("ranked:v2:team:" .. tostring(tk) .. ":ready")
    if err or not ready then return 0 end
    local n = 0
    for _ in pairs(ready) do n = n + 1 end
    return n
end

function execute(r)
    local tk = robot.get("rankedTeamKey")
    local roleId = tostring(robot.get("roleId"))
    if not tk then
        return robot.error(54, "RankedLeaderWaitReady 缺少 teamKey")
    end
    share.hash_set("ranked:v2:team:" .. tostring(tk) .. ":ready", roleId, "1", TEAM_TTL)
    local expected = tonumber(robot.get("rankedTeamSize") or 1)
    local waited = 0
    while waited < WAIT_MS do
        if readyCount(tk) >= expected then break end
        utils.sleep(POLL_MS)
        waited = waited + POLL_MS
    end
    if readyCount(tk) < expected then
        share.hash_set("ranked:v2:team:" .. tostring(tk), "status", "failed", TEAM_TTL)
        share.hash_set("ranked:v2:team:" .. tostring(tk), "failReason", "ready_timeout", TEAM_TTL)
        log.info("排位队长等准备超时，跳过本轮: teamKey=" .. tostring(tk))
        return robot.error(54, "RankedLeaderWaitReady 超时")
    end
    share.hash_set("ranked:v2:team:" .. tostring(tk), "status", "ready", TEAM_TTL)
    log.info("排位队长全员准备完成: teamKey=" .. tostring(tk) .. " size=" .. tostring(expected))
    return nil
end
