-- ranked_team_prepare.lua: 排位组队准备（v2）
-- 压测优先保证长期稳定运行：Redis 只做轻量协调，服务端业务失败记录后清理本轮并返回 0。
local share = require("share")
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local utils = require("utils")
local log = require("log")

local PREFIX = "ranked:v2"
local TEAM_TTL = 600
local QUEUE_TTL = 120
local CLAIM_TTL = 60
local WAIT_MARK_TTL = 30
local WAIT_INVITE_MS = 15000
local WAIT_JOIN_MS = 10000
local WAIT_JOINED_MS = 20000
local WAIT_READY_MS = 10000
local POLL_MS = 250
local STALE_QUEUE_MS = 60000

local function teamKey(key)
    return PREFIX .. ":team:" .. tostring(key)
end

local function currentTeamId()
    local tid = robot.get("teamId")
    if tid then return tid end

    local teamData = robot.get("teamData")
    if type(teamData) == "table" then
        if teamData.id then return teamData.id end
        if type(teamData.teamData) == "table" and teamData.teamData.id then
            return teamData.teamData.id
        end
    end
    return nil
end

local function clearKeys(keys)
    for _, k in ipairs(keys) do
        robot.delete(k)
    end
end

local function clearRoundState()
    clearKeys({
        "rankedMatchStarted",
        "battleId", "battleAddress", "battleSecretKey", "battleSession", "battleArea",
        "battleGameType", "fighterIndex", "fighterListData", "packageIndex",
        "battleAck", "loadProgress",
    })
end

local function clearTeamState()
    clearKeys({
        "rankedTeamSize", "rankedTeamRole", "rankedTeamKey", "rankedTeamInvite",
        "rankedTeamInviteReceived", "rankedTeamAcceptDone", "rankedTeamReady",
        "rankedTeamMembers", "rankedTeamLeaderId", "rankedTeamTargetSize",
        "rankedTeamDesiredSize", "rankedQueueToken", "teamId", "teamJoinCode",
        "teamModel", "teamGType", "teamModeId", "teamTsId", "teamData",
        "teamMemberCount", "teamHeaderId",
    })
end

local function safeLeaveTeam(reason)
    local tid = currentTeamId()
    if not tid then
        log.info("排位组队准备退出队伍: reason=" .. tostring(reason) .. " 本地无 teamId，跳过")
        return 0
    end

    local msg = proto.create("Game.TeamLeaveC2S")
    proto.set_field(msg, "teamId", tonumber(tid))
    local code = network.tcp_request("logic", {cmd=5, act=2}, msg, "Game.TeamLeaveS2C")
    log.info("排位组队准备退出队伍: reason=" .. tostring(reason)
        .. " teamId=" .. tostring(tid) .. " code=" .. tostring(code))
    robot.delete("teamId")
    robot.delete("teamData")
    robot.delete("teamMemberCount")
    robot.delete("teamHeaderId")
    return code
end

local function markRoundFailed(reason)
    robot.delete("rankedMatchStarted")
    robot.set("rankedRoundFailed", tostring(reason))
    clearRoundState()
    log.warn("排位组队准备: 本轮跳过匹配 reason=" .. tostring(reason))
end

local function setSoloState(teamId)
    robot.set("rankedTeamRole", "solo")
    robot.set("rankedTeamSize", 1)
    robot.set("rankedTeamTargetSize", 1)
    robot.set("rankedTeamDesiredSize", 1)
    robot.delete("rankedTeamKey")
    robot.delete("rankedTeamLeaderId")
    if teamId then robot.set("teamId", teamId) end
    robot.delete("rankedRoundFailed")
end

local function createTeamWithRetry(reason)
    local msg = proto.create("Game.TeamCreateC2S")
    proto.set_field(msg, "model", 2)
    local code, resp = network.tcp_request("logic", {cmd=5, act=1}, msg, "Game.TeamCreateS2C")
    if code == 0 then return code, resp end

    log.warn("排位创建队伍失败: reason=" .. tostring(reason) .. " code=" .. tostring(code) .. "，退出后重试一次")
    safeLeaveTeam("create_retry_after_" .. tostring(code))

    msg = proto.create("Game.TeamCreateC2S")
    proto.set_field(msg, "model", 2)
    code, resp = network.tcp_request("logic", {cmd=5, act=1}, msg, "Game.TeamCreateS2C")
    if code ~= 0 then
        log.warn("排位创建队伍重试失败: reason=" .. tostring(reason) .. " code=" .. tostring(code))
    end
    return code, resp
end

local function createSoloTeam(reason)
    local code, resp = createTeamWithRetry(reason)
    if code ~= 0 then
        clearTeamState()
        markRoundFailed("solo_create_failed_" .. tostring(code))
        return false
    end

    local tid = proto.get_field(resp, "teamId")
    setSoloState(tid)
    log.info("排位单排准备完成: teamId=" .. tostring(tid) .. " reason=" .. tostring(reason))
    return true
end

local function sendTeamPrepare(reason)
    local msg = proto.create("Game.TeamPrepareC2S")
    proto.set_field(msg, "isPrepare", true)
    local code = network.tcp_send("logic", {cmd=5, act=12}, msg)
    if code ~= 0 then
        log.warn("排位准备发送失败: reason=" .. tostring(reason) .. " code=" .. tostring(code))
        return false
    end
    return true
end

local function waitUntil(timeoutMs, fn)
    local waited = 0
    while waited < timeoutMs do
        if fn() then return true end
        utils.sleep(POLL_MS)
        waited = waited + POLL_MS
    end
    return false
end

-- v2 listen 迁移：listen 脚本回调已下线（framework/32），改为 memberPrepare 主流程
-- 主动 try_tcp_listen pop 缓存队列消费。drain* 在 waitUntil 循环里反复调用直到队列空。
local PROTO_INVITE = "Game.TeamNotifyInviteS2C"
local PROTO_JOIN = "Game.TeamJoinS2C"

-- drainInvite：非阻塞 pop TeamNotifyInvite 队列；仅 model==2 的排位邀请写 rankedTeamInvite。
local function drainInvite()
    local gotRanked = false
    while true do
        local code, raw = network.try_tcp_listen("logic", {cmd = 5, act = 6})
        if code == 31 then break end
        if code ~= 0 then
            log.warn("drainInvite: try_tcp_listen code=" .. tostring(code))
            break
        end
        if raw == nil or raw == "" then break end

        local ok, msg = pcall(proto.parse, PROTO_INVITE, raw)
        if not ok then
            log.warn("drainInvite: 解析失败丢弃 err=" .. tostring(msg))
        else
            local fm = proto.get_field_map(msg)
            if fm and tonumber(fm.model) == 2 then
                local inviterId = nil
                if type(fm.inviteInfo) == "table" then
                    inviterId = fm.inviteInfo.playerId
                end
                robot.set("rankedTeamInvite", {
                    teamId = fm.teamId,
                    model = fm.model,
                    gType = fm.gType,
                    battleModeId = fm.battleModeId,
                    roomId = fm.roomId,
                    isInvite = fm.isInvite,
                    inviterId = inviterId,
                    beInvite = fm.beInvite,
                })
                robot.set("rankedTeamInviteReceived", true)
                gotRanked = true
                log.info("drainInvite: 收到排位邀请 inviter=" .. tostring(inviterId)
                    .. " teamId=" .. tostring(fm.teamId))
            else
                log.debug("drainInvite: 非排位邀请 model=" .. tostring(fm and fm.model))
            end
        end
    end
    return gotRanked
end

-- drainJoin：非阻塞 pop TeamJoin 队列；code==0 置 rankedTeamAcceptDone 并写 Redis members 通知队长。
local function drainJoin(roleId, account)
    local joined = false
    while true do
        local code, raw = network.try_tcp_listen("logic", {cmd = 5, act = 10})
        if code == 31 then break end
        if code ~= 0 then
            log.warn("drainJoin: try_tcp_listen code=" .. tostring(code))
            break
        end
        if raw == nil or raw == "" then break end

        local ok, msg = pcall(proto.parse, PROTO_JOIN, raw)
        if not ok then
            log.warn("drainJoin: 解析失败丢弃 err=" .. tostring(msg))
        else
            local codeNum = tonumber(proto.get_field(msg, "code"))
            robot.set("teamJoinCode", codeNum or -1)
            local teamId = proto.get_field(msg, "teamId")
            if teamId then robot.set("teamId", tonumber(teamId) or teamId) end
            robot.set("teamModel", proto.get_field(msg, "model"))
            robot.set("teamGType", proto.get_field(msg, "gType"))
            robot.set("teamModeId", proto.get_field(msg, "modeId"))
            robot.set("teamTsId", proto.get_field(msg, "tsId"))

            if codeNum == 0 then
                robot.set("rankedTeamAcceptDone", true)
                joined = true
                local tk = robot.get("rankedTeamKey")
                if tk and tostring(tk) ~= "" then
                    local _, setErr = share.hash_set(teamKey(tostring(tk)) .. ":members", tostring(roleId), {
                        playerId = roleId,
                        account = account or "",
                        role = "member",
                        status = "joined",
                        teamId = teamId,
                        joinedAtMs = tostring(utils.time_ms()),
                    }, TEAM_TTL)
                    if setErr then
                        log.debug("drainJoin: 写 Redis members 失败（可忽略）err=" .. tostring(setErr))
                    end
                end
                log.info("drainJoin: 加入成功 teamId=" .. tostring(teamId))
            else
                log.warn("drainJoin: 加入失败 code=" .. tostring(codeNum))
            end
        end
    end
    return joined
end

-- drainUpdate：非阻塞 pop TeamUpdateInfo 队列清残留（teamData 由 Go 声明式 store 实时写，Lua 仅消费队列）。
local function drainUpdate()
    while true do
        local code = network.try_tcp_listen("logic", {cmd = 5, act = 3})
        if code == 31 then break end
        if code ~= 0 then break end
    end
end

local function chooseRole()
    local r = math.random(100)
    if r <= 20 then return "solo" end
    if r <= 60 then return "leader" end
    return "member"
end

local function chooseTargetSize()
    local r = math.random(100)
    if r <= 35 then return 1 end
    if r <= 80 then return 2 end
    return 3
end

local function makeQueueToken(roleId)
    return tostring(roleId) .. ":" .. tostring(utils.time_ms())
end

local function encodeQueueEntry(roleId, account, token)
    return tostring(roleId) .. "|" .. tostring(account or "") .. "|" .. tostring(token) .. "|" .. tostring(utils.time_ms())
end

local function parseQueueEntry(entry)
    if type(entry) ~= "string" then return nil end
    local roleId, account, token, enqueueMs = entry:match("^([^|]+)|([^|]*)|([^|]+)|([^|]+)$")
    if not roleId then return nil end
    return tonumber(roleId), account or "", token, tonumber(enqueueMs) or 0
end

local function writeTeamStatus(tk, status, reason)
    if not tk then return end
    local hashKey = teamKey(tk)
    share.hash_set(hashKey, "status", status, TEAM_TTL)
    share.hash_set(hashKey, "updatedAtMs", tostring(utils.time_ms()), TEAM_TTL)
    if reason then
        share.hash_set(hashKey, "failReason", tostring(reason), TEAM_TTL)
    end
end

local function waitMarkKey(roleId)
    return PREFIX .. ":wait:" .. tostring(roleId)
end

local function writeWaitMark(roleId, token, status)
    share.hash_set(waitMarkKey(roleId), "token", token or "", WAIT_MARK_TTL)
    share.hash_set(waitMarkKey(roleId), "status", status, WAIT_MARK_TTL)
    share.hash_set(waitMarkKey(roleId), "updatedAtMs", tostring(utils.time_ms()), WAIT_MARK_TTL)
end

local function isMemberStillWaiting(member)
    local mark, err = share.hash_get_all(waitMarkKey(member.roleId))
    if err or not mark then return false end
    return mark.status == "waiting" and tostring(mark.token or "") == tostring(member.token or "")
end

local function readClaim(roleId, token)
    local claim, err = share.hash_get_all(PREFIX .. ":claim:" .. tostring(roleId))
    if err or not claim then return nil end
    if token and claim.token and tostring(claim.token) ~= tostring(token) then
        return nil
    end
    return claim
end

local function waitForInvite(roleId, token)
    local invite = nil
    waitUntil(WAIT_INVITE_MS, function()
        local claim = readClaim(roleId, token)
        if claim then
            robot.set("rankedTeamKey", claim.teamKey)
            robot.set("rankedTeamLeaderId", tonumber(claim.leaderId or "0"))
            robot.set("rankedTeamTargetSize", tonumber(claim.targetSize or "1") or 1)
            if claim.teamId then robot.set("teamId", tonumber(claim.teamId) or claim.teamId) end
        end

        drainInvite()
        if robot.get("rankedTeamInviteReceived") then
            invite = robot.get("rankedTeamInvite")
            return invite ~= nil
        end
        return false
    end)
    return invite
end

local function acceptInvite(invite)
    local model = tonumber(invite and invite.model) or 0
    if model ~= 2 then
        log.warn("排位队员入队: 邀请模式非排位 model=" .. tostring(model))
        return false
    end

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
        log.warn("排位队员入队: TeamAccept 发送失败 code=" .. tostring(code))
        return false
    end
    return true
end

local function waitForJoin(roleId, account)
    return waitUntil(WAIT_JOIN_MS, function()
        drainJoin(roleId, account)
        return robot.get("rankedTeamAcceptDone") == true
    end)
end

local function memberPrepare(roleId, account)
    local token = makeQueueToken(roleId)
    robot.set("rankedQueueToken", token)
    robot.set("rankedTeamRole", "member")
    robot.delete("rankedRoundFailed")
    writeWaitMark(roleId, token, "waiting")

    local ok, err = share.queue_push(PREFIX .. ":waiting", encodeQueueEntry(roleId, account, token), QUEUE_TTL)
    if err or not ok then
        writeWaitMark(roleId, token, "queue_failed")
        log.warn("排位队员入队列失败: err=" .. tostring(err) .. "，改为单排")
        createSoloTeam("member_queue_failed")
        return 0
    end

    log.info("排位队员等待邀请: roleId=" .. tostring(roleId))
    local invite = waitForInvite(roleId, token)
    if not invite then
        writeWaitMark(roleId, token, "invite_timeout")
        log.info("排位队员等待邀请超时，改为单排 roleId=" .. tostring(roleId))
        createSoloTeam("member_invite_timeout")
        return 0
    end

    writeWaitMark(roleId, token, "accepting")
    if not acceptInvite(invite) then
        writeWaitMark(roleId, token, "accept_failed")
        safeLeaveTeam("member_accept_failed")
        createSoloTeam("member_accept_failed")
        return 0
    end

    if not waitForJoin(roleId, account) then
        writeWaitMark(roleId, token, "join_timeout")
        log.warn("排位队员等待入队确认超时 code=" .. tostring(robot.get("teamJoinCode")))
        safeLeaveTeam("member_join_timeout")
        markRoundFailed("member_join_timeout")
        return 0
    end

    writeWaitMark(roleId, token, "joined")
    if not sendTeamPrepare("member") then
        writeWaitMark(roleId, token, "prepare_failed")
        safeLeaveTeam("member_prepare_failed")
        markRoundFailed("member_prepare_failed")
        return 0
    end

    local teamKeyValue = robot.get("rankedTeamKey")
    if teamKeyValue then
        share.hash_set(teamKey(teamKeyValue) .. ":ready", tostring(roleId), "1", TEAM_TTL)
        share.hash_set(teamKey(teamKeyValue) .. ":members", tostring(roleId), {
            playerId = roleId,
            account = account,
            role = "member",
            status = "ready",
            readyAtMs = tostring(utils.time_ms()),
        }, TEAM_TTL)
    end

    writeWaitMark(roleId, token, "ready")
    robot.set("rankedTeamRole", "member")
    robot.delete("rankedRoundFailed")
    log.info("排位队员准备完成: roleId=" .. tostring(roleId)
        .. " teamKey=" .. tostring(teamKeyValue)
        .. " teamId=" .. tostring(robot.get("teamId")))
    return 0
end

local function popMembers(targetSize, leaderRoleId)
    local members = {}
    local seen = {}
    local need = targetSize - 1
    local tries = need * 6
    local now = utils.time_ms()

    while #members < need and tries > 0 do
        tries = tries - 1
        local entry, ok, err = share.queue_pop(PREFIX .. ":waiting")
        if err or not ok or not entry then break end

        local memberRoleId, memberAccount, token, enqueueMs = parseQueueEntry(entry)
        if memberRoleId and memberRoleId ~= leaderRoleId and not seen[memberRoleId]
            and (enqueueMs == 0 or now - enqueueMs <= STALE_QUEUE_MS) then
            local member = {
                roleId = memberRoleId,
                account = memberAccount,
                token = token,
            }
            if isMemberStillWaiting(member) then
                seen[memberRoleId] = true
                table.insert(members, member)
            else
                log.info("排位队长跳过过期队员: target=" .. tostring(memberRoleId)
                    .. " token=" .. tostring(token))
            end
        end
    end
    return members
end

local function sendInvite(member)
    local msg = proto.create("Game.TeamInviteC2S")
    proto.set_field(msg, "beInviter", member.roleId)
    proto.set_field(msg, "isInvite", true)
    local code, resp = network.tcp_request("logic", {cmd=5, act=4}, msg, "Game.TeamInviteS2C")
    if code ~= 0 then
        local raw = ""
        if type(resp) == "string" then raw = " raw=" .. tostring(#resp) .. "B" end
        log.warn("排位队长邀请失败: target=" .. tostring(member.roleId)
            .. " code=" .. tostring(code) .. raw)
        return false
    end
    log.info("排位队长邀请发送: target=" .. tostring(member.roleId))
    return true
end

local function waitForJoinedCount(tk, expected)
    local function countJoined()
        local members, err = share.hash_get_all(teamKey(tk) .. ":members")
        if err or not members then return 0 end
        local count = 0
        for _, member in pairs(members) do
            if type(member) == "table" and (member.status == "joined" or member.status == "ready") then
                count = count + 1
            end
        end
        return count
    end

    waitUntil(WAIT_JOINED_MS, function()
        return countJoined() >= expected
    end)

    local count = countJoined()
    if count <= 0 then count = 1 end
    if count > expected then count = expected end
    return count
end

local function waitReadyCount(tk, expected)
    if expected <= 1 then return true end
    return waitUntil(WAIT_READY_MS, function()
        local ready, err = share.hash_get_all(teamKey(tk) .. ":ready")
        if err or not ready then return false end
        local count = 0
        for _, _ in pairs(ready) do count = count + 1 end
        return count >= expected
    end)
end

local function leaderPrepare(roleId, account)
    local code, resp = createTeamWithRetry("leader")
    if code ~= 0 then
        markRoundFailed("leader_create_failed_" .. tostring(code))
        return 0
    end

    local tid = proto.get_field(resp, "teamId")
    robot.set("teamId", tid)

    local targetSize = chooseTargetSize()
    if targetSize <= 1 then
        setSoloState(tid)
        log.info("排位队长决定单排: roleId=" .. tostring(roleId) .. " teamId=" .. tostring(tid))
        return 0
    end

    local seq, seqErr = share.incr(PREFIX .. ":seq")
    if seqErr then
        log.warn("排位队长获取队伍序号失败: err=" .. tostring(seqErr) .. "，改为单排")
        setSoloState(tid)
        return 0
    end

    local tk = tostring(seq)
    local hashKey = teamKey(tk)
    local memberKey = hashKey .. ":members"
    share.hash_set(hashKey, "status", "forming", TEAM_TTL)
    share.hash_set(hashKey, "leaderId", roleId, TEAM_TTL)
    share.hash_set(hashKey, "leaderAccount", account, TEAM_TTL)
    share.hash_set(hashKey, "teamId", tostring(tid), TEAM_TTL)
    share.hash_set(hashKey, "targetSize", targetSize, TEAM_TTL)
    share.hash_set(hashKey, "actualSize", 1, TEAM_TTL)
    share.hash_set(hashKey, "createdAtMs", tostring(utils.time_ms()), TEAM_TTL)
    share.hash_set(memberKey, tostring(roleId), {
        playerId = roleId,
        account = account,
        role = "leader",
        status = "joined",
    }, TEAM_TTL)

    robot.set("rankedTeamKey", tk)
    robot.set("rankedTeamRole", "leader")
    robot.set("rankedTeamLeaderId", roleId)
    robot.set("rankedTeamDesiredSize", targetSize)
    robot.set("rankedTeamTargetSize", targetSize)
    robot.delete("rankedRoundFailed")

    local candidates = popMembers(targetSize, roleId)
    local invited = 0
    for _, member in ipairs(candidates) do
        if not isMemberStillWaiting(member) then
            log.info("排位队长邀请前跳过过期队员: target=" .. tostring(member.roleId)
                .. " token=" .. tostring(member.token))
        else
            share.hash_set(PREFIX .. ":claim:" .. tostring(member.roleId), "teamKey", tk, CLAIM_TTL)
            share.hash_set(PREFIX .. ":claim:" .. tostring(member.roleId), "teamId", tostring(tid), CLAIM_TTL)
            share.hash_set(PREFIX .. ":claim:" .. tostring(member.roleId), "leaderId", tostring(roleId), CLAIM_TTL)
            share.hash_set(PREFIX .. ":claim:" .. tostring(member.roleId), "targetSize", targetSize, CLAIM_TTL)
            share.hash_set(PREFIX .. ":claim:" .. tostring(member.roleId), "token", member.token or "", CLAIM_TTL)
            writeWaitMark(member.roleId, member.token, "claimed")

            if sendInvite(member) then
                invited = invited + 1
                writeWaitMark(member.roleId, member.token, "invited")
                share.hash_set(memberKey, tostring(member.roleId), {
                    playerId = member.roleId,
                    account = member.account,
                    role = "member",
                    status = "invited",
                    inviteAtMs = tostring(utils.time_ms()),
                }, TEAM_TTL)
            else
                writeWaitMark(member.roleId, member.token, "invite_failed")
            end
        end
    end

    share.hash_set(hashKey, "status", "inviting", TEAM_TTL)
    share.hash_set(hashKey, "invitedCount", invited, TEAM_TTL)

    local expected = 1 + invited
    local actualSize = waitForJoinedCount(tk, expected)
    if actualSize <= 1 then
        setSoloState(tid)
        writeTeamStatus(tk, "done", "no_member_joined")
        log.info("排位队长未形成多排，使用已有队伍单排: target=" .. tostring(targetSize)
            .. " invited=" .. tostring(invited))
        return 0
    end

    robot.set("rankedTeamRole", "leader")
    robot.set("rankedTeamKey", tk)
    robot.set("rankedTeamSize", actualSize)
    robot.set("rankedTeamTargetSize", actualSize)
    share.hash_set(hashKey, "actualSize", actualSize, TEAM_TTL)

    if not sendTeamPrepare("leader") then
        writeTeamStatus(tk, "failed", "leader_prepare_failed")
        safeLeaveTeam("leader_prepare_failed")
        markRoundFailed("leader_prepare_failed")
        return 0
    end

    share.hash_set(hashKey .. ":ready", tostring(roleId), "1", TEAM_TTL)
    if not waitReadyCount(tk, actualSize) then
        writeTeamStatus(tk, "failed", "ready_timeout")
        safeLeaveTeam("ready_timeout")
        markRoundFailed("ready_timeout")
        return 0
    end

    writeTeamStatus(tk, "ready", nil)
    robot.delete("rankedRoundFailed")
    log.info("排位队长准备完成: teamKey=" .. tostring(tk)
        .. " teamId=" .. tostring(tid)
        .. " target=" .. tostring(targetSize)
        .. " actual=" .. tostring(actualSize)
        .. " invited=" .. tostring(invited))
    return 0
end

function execute(r)
    local roleId = tonumber(robot.get("roleId"))
    local account = robot.get("account") or ""
    if not roleId then
        log.error("ranked_team_prepare: roleId 为空，本轮跳过")
        markRoundFailed("prepare_no_role_id")
        return 0
    end

    -- 入口清 Go 侧 listenQueues 物理残留（上一轮未消费的 invite/update 消息），再清 state。
    drainInvite()
    drainUpdate()

    robot.delete("rankedRoundFailed")
    robot.delete("rankedMatchStarted")
    robot.delete("rankedTeamInvite")
    robot.delete("rankedTeamInviteReceived")
    robot.delete("rankedTeamAcceptDone")
    robot.delete("teamJoinCode")
    clearRoundState()

    local role = chooseRole()
    log.info("排位组队准备: roleId=" .. tostring(roleId) .. " role=" .. tostring(role))

    if role == "solo" then
        createSoloTeam("random_solo")
        return 0
    end
    if role == "member" then
        return memberPrepare(roleId, account)
    end
    return leaderPrepare(roleId, account)
end
