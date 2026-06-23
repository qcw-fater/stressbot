-- ranked_leader_recruit.lua: 队长等待成员 + 发送邀请
-- 等待 Redis memberCount 达标，然后读取成员列表发送 TeamInviteC2S。
-- 邀请链路不完整时标记本轮失败，避免后续 ready/match 阶段靠超时兜底。
local share = require("share")
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local utils = require("utils")
local log = require("log")

local TEAM_TTL = 600     -- TEAM_TTL: 所有排位脚本统一 600s
local TEAM_LOCK_TTL = 5
local WAIT_MS = 30000    -- 等 Redis 成员达标 30s

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
        log.info("排位队长招募清理队伍: reason=" .. tostring(reason)
            .. " teamId=" .. tostring(tid) .. " code=" .. codeText)
    end
    robot.delete("teamId")
    robot.delete("teamData")
    robot.delete("teamMemberCount")
    robot.delete("teamHeaderId")
end

local function degradeSolo(hashKey, reason)
    if hashKey then
        share.hash_set(hashKey, "status", "done", TEAM_TTL)
        share.hash_set(hashKey, "failReason", tostring(reason), TEAM_TTL)
    end
    robot.delete("rankedRoundFailed")
    robot.set("rankedTeamRole", "solo")
    robot.set("rankedTeamSize", 1)
    robot.set("rankedTeamTargetSize", 1)
    robot.delete("rankedTeamKey")
    robot.delete("rankedTeamLeaderId")
    leaveAndClearTeam(reason)
end

function execute(r)
    local teamKey = robot.get("rankedTeamKey")
    if not teamKey then
        log.warn("排位队长招募: teamKey 为空，降级单排继续")
        robot.delete("rankedRoundFailed")
        robot.set("rankedTeamRole", "solo")
        robot.set("rankedTeamSize", 1)
        robot.set("rankedTeamTargetSize", 1)
        return nil
    end

    local targetSize = tonumber(robot.get("rankedTeamTargetSize") or robot.get("rankedTeamSize") or 1)
    local account = robot.get("account") or ""
    local hashKey = "ranked:v1:team:" .. teamKey

    -- ========== 等待成员 ==========
    local waited = 0
    while waited < WAIT_MS do
        local countVal, _, err = share.hash_get(hashKey, "memberCount")
        if err then break end
        local memberCount = tonumber(countVal) or 1
        if memberCount >= targetSize then break end
        utils.sleep(1000)
        waited = waited + 1000
    end

    -- 检查最终成员数
    local countVal, _, _ = share.hash_get(hashKey, "memberCount")
    local memberCount = tonumber(countVal) or 1

    if memberCount < 2 then
        -- 只有队长自己时，已有服务端队伍仍有效，可以降为本轮单排继续。
        log.warn("排位队长招募: 无队员，用已有队伍单排")
        robot.set("rankedTeamRole", "solo")
        robot.set("rankedTeamSize", 1)
        robot.set("rankedTeamTargetSize", 1)
        robot.delete("rankedRoundFailed")
        return nil
    end

    -- 有人但不够：调整目标人数
    if memberCount < targetSize then
        log.info("排位队长招募: 人不够但直接开始 count=" .. tostring(memberCount)
            .. "/" .. tostring(targetSize))
        robot.set("rankedTeamTargetSize", memberCount)
        robot.set("rankedTeamSize", memberCount)
        targetSize = memberCount
    else
        log.info("排位队长招募: 成员已满 count=" .. tostring(memberCount))
    end

    -- ========== 发送邀请 ==========
    local lockKey = "ranked:v1:team:" .. teamKey .. ":lock"
    local ok, err = share.claim(lockKey, account, TEAM_LOCK_TTL)
    if err or not ok then
        log.warn("排位队长招募: 获取队伍锁失败，降级单排继续")
        degradeSolo(hashKey,"leader_recruit_lock_failed")
        return nil
    end

    local memberHashKey = "ranked:v1:team:" .. teamKey .. ":members"
    local members, merr = share.hash_get_all(memberHashKey)
    share.release(lockKey, account)

    if merr or not members then
        log.warn("排位队长招募: 读取成员列表失败，降级单排继续")
        degradeSolo(hashKey,"leader_recruit_members_read_failed")
        return nil
    end

    local roleId = tonumber(robot.get("roleId"))
    local invited = 0
    for playerIdStr, memberData in pairs(members) do
        local pid = tonumber(playerIdStr)
        if pid and pid ~= roleId then
            local msg = proto.create("Game.TeamInviteC2S")
            proto.set_field(msg, "beInviter", pid)
            proto.set_field(msg, "isInvite", true)
            local err, resp = network.tcp_request("logic", {cmd=5, act=4}, msg, "Game.TeamInviteS2C")
            if not err then
                invited = invited + 1
                local respData = ""
                if resp then
                    local fm = proto.get_field_map(resp)
                    if fm then
                        respData = " err=" .. tostring(fm.error or fm.err or fm.code or "nil")
                    end
                end
                log.info("排位队长招募: 邀请发送 target=" .. tostring(pid) .. respData)
            else
                local respData = ""
                if resp then
                    respData = " raw=" .. tostring(string.len(resp)) .. "B"
                end
                log.warn("排位队长招募: 邀请失败 target=" .. tostring(pid)
                    .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail) .. respData)
            end
        end
    end

    local expectedInvites = targetSize - 1
    if invited < expectedInvites then
        log.warn("排位队长招募: 邀请未全部成功 invited=" .. tostring(invited)
            .. "/" .. tostring(expectedInvites) .. "，降级单排继续")
        degradeSolo(hashKey,"leader_recruit_invite_incomplete")
        return nil
    end

    share.hash_set(hashKey, "status", "inviting", TEAM_TTL)
    robot.delete("rankedRoundFailed")
    log.info("排位队长招募: 完成 invited=" .. tostring(invited))
    return nil
end
