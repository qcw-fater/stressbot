-- ranked_member_wait_invite.lua: 队员轮询 Redis claim + drain TeamNotifyInvite(5:6)
-- 收到排位邀请(model==2) → 存 rankedTeamInvite + 从 claim 取 teamKey，返回 nil
-- 超时 / 队长标记邀请失败 → 返回 err(skip)，不降级单排。teamId 由 invite 提供（accept 用），不从 claim 预设。
local share = require("share")
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local utils = require("utils")
local log = require("log")

local PREFIX = "ranked:v2"
local WAIT_MARK_TTL = 30
local WAIT_MS = 25000
local POLL_MS = 250
local PROTO_INVITE = "Game.TeamNotifyInviteS2C"

local function waitMarkKey(id) return PREFIX .. ":wait:" .. tostring(id) end

local function readClaim(roleId, token)
    local claim, err = share.hash_get_all(PREFIX .. ":claim:" .. tostring(roleId))
    if err or not claim then return nil end
    if token and claim.token and tostring(claim.token) ~= tostring(token) then return nil end
    return claim
end

local function drainInvite()
    local got = nil
    while true do
        local err, raw = network.try_tcp_listen("logic", {cmd=5, act=6})
        if err and err.code == 31 then break end
        if err then break end
        if raw == nil or raw == "" then break end
        local ok, msg = pcall(proto.parse, PROTO_INVITE, raw)
        if not ok then
            log.warn("drainInvite 解析失败丢弃: err=" .. tostring(msg))
        else
            local fm = proto.get_field_map(msg)
            if fm and tonumber(fm.model) == 2 then
                local inviterId = nil
                if type(fm.inviteInfo) == "table" then inviterId = fm.inviteInfo.playerId end
                got = {
                    teamId = fm.teamId, model = fm.model, gType = fm.gType,
                    battleModeId = fm.battleModeId, roomId = fm.roomId,
                    isInvite = fm.isInvite, inviterId = inviterId, beInvite = fm.beInvite,
                }
            end
        end
    end
    return got
end

function execute(r)
    local roleId = tonumber(robot.get("roleId"))
    local token = robot.get("rankedQueueToken")
    if not roleId then
        return robot.error(54, "RankedMemberWaitInvite 缺少 roleId")
    end
    local deadline = utils.time_ms() + WAIT_MS
    local invite = nil
    while utils.time_ms() < deadline do
        local claim = readClaim(roleId, token)
        if claim then
            robot.set("rankedTeamKey", claim.teamKey)
            robot.set("rankedTeamLeaderId", tonumber(claim.leaderId or "0") or 0)
        end
        invite = drainInvite()
        if invite then break end
        local mark = share.hash_get_all(waitMarkKey(roleId))
        if mark and (mark.status == "invite_failed" or mark.status == "failed") then
            log.info("排位队员邀请失败标记，跳过本轮: roleId=" .. tostring(roleId))
            return robot.error(54, "RankedMemberWaitInvite 邀请失败")
        end
        utils.sleep(POLL_MS)
    end
    if not invite then
        share.hash_set(waitMarkKey(roleId), "status", "invite_timeout", WAIT_MARK_TTL)
        log.info("排位队员等邀请超时，跳过本轮: roleId=" .. tostring(roleId))
        return robot.error(54, "RankedMemberWaitInvite 超时")
    end
    robot.set("rankedTeamInvite", invite)
    share.hash_set(waitMarkKey(roleId), "status", "accepting", WAIT_MARK_TTL)
    log.info("排位队员收到邀请: roleId=" .. tostring(roleId) .. " teamId=" .. tostring(invite.teamId))
    return nil
end
