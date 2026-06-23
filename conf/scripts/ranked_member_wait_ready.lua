-- ranked_member_wait_ready.lua: 队员等待队伍就绪
-- 仅队员角色执行。先发送 TeamPrepareC2S 通知服务端，再标记 Redis 就绪，然后轮询 Redis 队伍状态。
-- 成功时保持 member 多排；失败/超时时降级单排继续。
local share = require("share")
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local utils = require("utils")
local log = require("log")

local TEAM_TTL = 600     -- TEAM_TTL: 所有排位脚本统一 600s
local TIMEOUT_MS = 60000 -- 等队伍就绪 60s，给多排协作足够时间

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

local function leaveAndClearTeam(reason)
    local tid = currentTeamId()
    if tid then
        local msg = proto.create("Game.TeamLeaveC2S")
        proto.set_field(msg, "teamId", tonumber(tid))
        local err = network.tcp_request("logic", {cmd=5, act=2}, msg, "Game.TeamLeaveS2C")
        local codeText = err and (tostring(err.code) .. " " .. tostring(err.detail)) or "0"
        log.info("排位队员等就绪清理队伍: reason=" .. tostring(reason)
            .. " teamId=" .. tostring(tid) .. " code=" .. codeText)
    end
    robot.delete("teamId")
    robot.delete("teamData")
    robot.delete("teamMemberCount")
    robot.delete("teamHeaderId")
end

local function degradeSolo(reason)
    robot.delete("rankedRoundFailed")
    robot.set("rankedTeamRole", "solo")
    robot.set("rankedTeamSize", 1)
    robot.set("rankedTeamTargetSize", 1)
    robot.delete("rankedTeamKey")
    robot.delete("rankedTeamLeaderId")
    leaveAndClearTeam(reason)
    log.warn("排位队员等就绪: " .. tostring(reason) .. "，降级单排继续")
end

function execute(r)
    local teamKey = robot.get("rankedTeamKey")
    if not teamKey then
        degradeSolo("teamKey 为空")
        return nil
    end

    local roleId = tostring(robot.get("roleId"))

    -- 通知服务端自己已准备（必须在 Redis 标记之前，确保 leader 看到 Redis ready 时服务端已处理）
    local msg = proto.create("Game.TeamPrepareC2S")
    proto.set_field(msg, "isPrepare", true)
    local err = network.tcp_send("logic", {cmd=5, act=12}, msg)
    if err then
        share.hash_set("ranked:v1:team:" .. teamKey, "status", "done", TEAM_TTL)
        share.hash_set("ranked:v1:team:" .. teamKey, "failReason", "member_prepare_send_failed_" .. tostring(err.code), TEAM_TTL)
        degradeSolo("TeamPrepare 发送失败 code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return nil
    else
        log.info("排位队员等就绪: TeamPrepare 已发送")
    end

    -- 标记自己就绪（Redis）
    local readyKey = "ranked:v1:team:" .. teamKey .. ":ready"
    share.hash_set(readyKey, roleId, "1", TEAM_TTL)

    -- 轮询等待队长标记队伍 ready
    local hashKey = "ranked:v1:team:" .. teamKey
    local waited = 0
    while waited < TIMEOUT_MS do
        local status, _, _ = share.hash_get(hashKey, "status")
        if status == "ready" or status == "matching" then
            robot.delete("rankedRoundFailed")
            log.info("排位队员等就绪: 队伍已就绪")
            return nil
        end
        if status == "failed" or status == "done" then
            degradeSolo("队伍已结束 status=" .. tostring(status))
            return nil
        end
        utils.sleep(500)
        waited = waited + 500
    end

    degradeSolo("超时")
    return nil
end
