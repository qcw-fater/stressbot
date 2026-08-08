-- ranked_leader_recruit.lua: 队长选 targetSize、写队伍协调状态、轮询待命队列招队员发邀请
-- 前置 RankedCreateTeam 已 store teamId。0 成员 → 置 Redis failed + 返回 err 触发 skip（不降级单排）。
-- 轮询 RECRUIT_MS 给池子成形时间（旧实现空队列即 break 导致招不到就单排，是"越跑越单排"成因之一）。
local share = require("share")
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local utils = require("utils")
local log = require("log")

local PREFIX = "ranked:v2"
local TEAM_TTL = 600
local CLAIM_TTL = 60
local WAIT_MARK_TTL = 30
local RECRUIT_MS = 25000
local POLL_MS = 250
local STALE_MS = 60000

local function teamKey(k) return PREFIX .. ":team:" .. tostring(k) end
local function waitMarkKey(id) return PREFIX .. ":wait:" .. tostring(id) end
local function claimKey(id) return PREFIX .. ":claim:" .. tostring(id) end

local function isMemberWaiting(roleId, token)
    local mark, err = share.hash_get_all(waitMarkKey(roleId))
    if err or not mark then return false end
    return mark.status == "waiting" and tostring(mark.token or "") == tostring(token or "")
end

local function sendInvite(targetRoleId)
    local msg = proto.create("Game.TeamInviteC2S")
    proto.set_field(msg, "beInviter", targetRoleId)
    proto.set_field(msg, "isInvite", true)
    local err = network.tcp_request("logic", {cmd=5, act=4}, msg, "Game.TeamInviteS2C")
    if err then
        log.warn("排位队长邀请失败: target=" .. tostring(targetRoleId) .. " code=" .. tostring(err.code))
        return false
    end
    return true
end

function execute(r)
    local roleId = tonumber(robot.get("roleId"))
    local account = robot.get("account") or ""
    local teamId = robot.get("teamId")
    if not teamId then
        return robot.error(54, "RankedLeaderRecruit 缺少 teamId")
    end

    local seq, seqErr = share.incr(PREFIX .. ":seq")
    if seqErr then
        return robot.error(54, "RankedLeaderRecruit seq 失败: " .. tostring(seqErr))
    end
    local tk = tostring(seq)
    local targetSize = (math.random(100) <= 30) and 3 or 2
    local hashKey = teamKey(tk)
    local memberKey = hashKey .. ":members"

    share.hash_set(hashKey, "status", "forming", TEAM_TTL)
    share.hash_set(hashKey, "leaderId", tostring(roleId), TEAM_TTL)
    share.hash_set(hashKey, "teamId", tostring(teamId), TEAM_TTL)
    share.hash_set(hashKey, "targetSize", tostring(targetSize), TEAM_TTL)
    share.hash_set(hashKey, "actualSize", "1", TEAM_TTL)
    share.hash_set(memberKey, tostring(roleId), {
        playerId = roleId, account = account, role = "leader", status = "joined",
    }, TEAM_TTL)

    robot.set("rankedTeamKey", tk)
    robot.set("rankedTeamLeaderId", roleId)
    robot.set("rankedTeamTargetSize", targetSize)
    robot.set("rankedTeamDesiredSize", targetSize)
    robot.delete("rankedRoundFailed")

    local need = targetSize - 1
    local invited = 0
    local seen = {}
    local deadline = utils.time_ms() + RECRUIT_MS
    local now = utils.time_ms()

    while invited < need and utils.time_ms() < deadline do
        local entry, ok = share.queue_pop(PREFIX .. ":waiting")
        if ok and entry and type(entry) == "string" then
            local mRoleId, mAccount, token, enqueueMs = entry:match("^([^|]+)|([^|]*)|([^|]+)|([^|]+)$")
            mRoleId = tonumber(mRoleId)
            local enq = tonumber(enqueueMs) or 0
            if mRoleId and mRoleId ~= roleId and not seen[mRoleId] and (enq == 0 or now - enq <= STALE_MS) then
                seen[mRoleId] = true
                if isMemberWaiting(mRoleId, token) then
                    share.hash_set(claimKey(mRoleId), "teamKey", tk, CLAIM_TTL)
                    share.hash_set(claimKey(mRoleId), "teamId", tostring(teamId), CLAIM_TTL)
                    share.hash_set(claimKey(mRoleId), "leaderId", tostring(roleId), CLAIM_TTL)
                    share.hash_set(claimKey(mRoleId), "targetSize", tostring(targetSize), CLAIM_TTL)
                    share.hash_set(claimKey(mRoleId), "token", token or "", CLAIM_TTL)
                    share.hash_set(waitMarkKey(mRoleId), "status", "claimed", WAIT_MARK_TTL)
                    if sendInvite(mRoleId) then
                        invited = invited + 1
                        share.hash_set(waitMarkKey(mRoleId), "status", "invited", WAIT_MARK_TTL)
                        share.hash_set(memberKey, tostring(mRoleId), {
                            playerId = mRoleId, account = mAccount, role = "member", status = "invited",
                        }, TEAM_TTL)
                    else
                        share.hash_set(waitMarkKey(mRoleId), "status", "invite_failed", WAIT_MARK_TTL)
                    end
                end
            end
        end
        utils.sleep(POLL_MS)
    end

    share.hash_set(hashKey, "invitedCount", tostring(invited), TEAM_TTL)

    if invited == 0 then
        share.hash_set(hashKey, "status", "failed", TEAM_TTL)
        share.hash_set(hashKey, "failReason", "no_member", TEAM_TTL)
        log.info("排位队长未招到队员，跳过本轮: teamId=" .. tostring(teamId))
        return robot.error(54, "RankedLeaderRecruit 无队员")
    end

    share.hash_set(hashKey, "status", "inviting", TEAM_TTL)
    robot.set("rankedTeamSize", 1 + invited)
    log.info("排位队长招募完成: teamKey=" .. tostring(tk) .. " invited=" .. tostring(invited))
    return nil
end
