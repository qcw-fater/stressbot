-- ranked_member_join.lua: 队员等待邀请 + 接受入队
-- 等待 listen_team_notify_invite 回调 → 校验 → 发 TeamAcceptC2S → 等待 TeamJoinS2C 回调。
-- 任一阶段失败优先降级单排继续，避免组队不稳定直接挡住开始战斗。
local share = require("share")
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local utils = require("utils")
local log = require("log")

local WAIT_INVITE_MS = 60000  -- 等邀请 60s，给多排协作足够时间
local WAIT_JOIN_MS = 45000    -- 等入队确认 45s，避免过早判定多排失败
local TEAM_TTL = 600

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
        local code = network.tcp_request("logic", {cmd=5, act=2}, msg, "Game.TeamLeaveS2C")
        log.info("排位队员入队清理队伍: reason=" .. tostring(reason)
            .. " teamId=" .. tostring(tid) .. " code=" .. tostring(code))
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
    robot.delete("rankedTeamInvite")
    robot.delete("rankedTeamInviteReceived")
    robot.delete("rankedTeamAcceptDone")
    robot.delete("teamJoinCode")
    leaveAndClearTeam(reason)
    log.warn("排位队员入队: " .. tostring(reason) .. "，降级单排继续")
end

function execute(r)
    local teamKey = robot.get("rankedTeamKey")
    local hashKey = nil
    if teamKey then
        hashKey = "ranked:v1:team:" .. tostring(teamKey)
    end

    -- ========== 等待邀请 ==========
    local waited = 0
    local invite = nil
    while waited < WAIT_INVITE_MS do
        if hashKey then
            local status, _, _ = share.hash_get(hashKey, "status")
            if status == "failed" or status == "done" then
                degradeSolo("队伍已结束 status=" .. tostring(status))
                return 0
            end
        end
        local received = robot.get("rankedTeamInviteReceived")
        invite = robot.get("rankedTeamInvite")
        if received and invite then break end
        utils.sleep(500)
        waited = waited + 500
    end

    if not invite then
        degradeSolo("等邀请超时")
        return 0
    end

    log.info("排位队员入队: 收到邀请 inviter=" .. tostring(invite.inviterId))

    -- ========== 校验 ==========
    local inviteModel = tonumber(invite.model) or 0
    if inviteModel ~= 2 then
        degradeSolo("邀请模式非排位 model=" .. tostring(inviteModel))
        return 0
    end

    -- ========== 接受邀请 ==========
    local msg = proto.create("Game.TeamAcceptC2S")
    proto.set_field(msg, "operation", 1)
    proto.set_field(msg, "teamId", invite.teamId)
    proto.set_field(msg, "inviter", invite.inviterId)
    proto.set_field(msg, "model", invite.model)
    proto.set_field(msg, "gType", invite.gType or 0)
    proto.set_field(msg, "battleModeId", invite.battleModeId or 0)
    proto.set_field(msg, "roomId", invite.roomId or 0)

    robot.delete("rankedTeamAcceptDone")
    robot.delete("teamJoinCode")

    local code = network.tcp_send("logic", {cmd=5, act=8}, msg)
    if code ~= 0 then
        if hashKey then
            share.hash_set(hashKey, "status", "done", TEAM_TTL)
            share.hash_set(hashKey, "failReason", "member_join_accept_failed_" .. tostring(code), TEAM_TTL)
        end
        degradeSolo("发送 TeamAccept 失败 code=" .. tostring(code))
        return 0
    end

    -- ========== 等待入队确认 ==========
    waited = 0
    while not robot.get("rankedTeamAcceptDone") and waited < WAIT_JOIN_MS do
        if hashKey then
            local status, _, _ = share.hash_get(hashKey, "status")
            if status == "failed" or status == "done" then
                degradeSolo("等入队确认时队伍已结束 status=" .. tostring(status))
                return 0
            end
        end
        utils.sleep(500)
        waited = waited + 500
    end

    if not robot.get("rankedTeamAcceptDone") then
        local joinCode = robot.get("teamJoinCode")
        if hashKey then
            share.hash_set(hashKey, "status", "done", TEAM_TTL)
            share.hash_set(hashKey, "failReason", "member_join_confirm_failed_" .. tostring(joinCode or "timeout"), TEAM_TTL)
        end
        degradeSolo("等待入队确认失败 code=" .. tostring(joinCode or "timeout"))
        return 0
    end

    robot.delete("rankedRoundFailed")
    log.info("排位队员入队: 成功 teamId=" .. tostring(robot.get("teamId")))
    return 0
end
