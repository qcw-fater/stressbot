-- ranked_member_mark_ready.lua: 队员 TeamPrepare 后写 Redis :ready + members status=ready
-- 供队长 RankedLeaderWaitReady 轮询 :ready 计数。
local share = require("share")
local robot = require("robot")
local utils = require("utils")
local log = require("log")

local TEAM_TTL = 600

function execute(r)
    local tk = robot.get("rankedTeamKey")
    local roleId = tostring(robot.get("roleId"))
    local account = robot.get("account") or ""
    if not tk then
        return robot.error(54, "RankedMemberMarkReady 缺少 teamKey")
    end
    share.hash_set("ranked:v2:team:" .. tostring(tk) .. ":ready", roleId, "1", TEAM_TTL)
    share.hash_set("ranked:v2:team:" .. tostring(tk) .. ":members", roleId, {
        playerId = roleId, account = account, role = "member", status = "ready",
        readyAtMs = tostring(utils.time_ms()),
    }, TEAM_TTL)
    share.hash_set("ranked:v2:wait:" .. roleId, "status", "ready", 30)
    log.info("排位队员准备完成: roleId=" .. tostring(roleId) .. " teamKey=" .. tostring(tk))
    return nil
end
