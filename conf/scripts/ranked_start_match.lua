-- ranked_start_match.lua: 单排/队长发送 TeamStartMatchC2S
-- 失败时立即标记本轮不可进入匹配后续阶段，并尽力退出队伍，避免 386 继续扩散为 288/Loading 超时。
local share = require("share")
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local utils = require("utils")
local log = require("log")

local TEAM_TTL = 600
local TEAM_PREFIX = "ranked:v2:team:"

local function clearBattleState()
    local keys = {
        "battleId",
        "battleAddress",
        "battleSecretKey",
        "battleSession",
        "fighterIndex",
        "fighterListData",
        "packageIndex",
        "battleAck",
        "loadProgress",
    }
    for _, k in ipairs(keys) do
        robot.delete(k)
    end
end

local function listToString(list)
    if type(list) ~= "table" then
        return tostring(list)
    end
    local parts = {}
    for _, v in ipairs(list) do
        table.insert(parts, tostring(v))
    end
    return table.concat(parts, ",")
end

local function mapToString(map)
    if type(map) ~= "table" then
        return tostring(map)
    end
    local parts = {}
    for k, v in pairs(map) do
        table.insert(parts, tostring(k) .. "=" .. tostring(v))
    end
    return table.concat(parts, ",")
end

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

local function leaveCurrentTeam(reason)
    local tid = currentTeamId()
    local msg = proto.create("Game.TeamLeaveC2S")
    if tid then
        proto.set_field(msg, "teamId", tonumber(tid))
    end
    local err = network.tcp_request("logic", {cmd=5, act=2}, msg, "Game.TeamLeaveS2C")
    local codeText = err and (tostring(err.code) .. " " .. tostring(err.detail)) or "0"
    log.info("排位开始匹配清理退出队伍: reason=" .. tostring(reason)
        .. " teamId=" .. tostring(tid) .. " code=" .. codeText)
    robot.delete("teamId")
end

local function markRoundNotStarted(reason)
    robot.delete("rankedMatchStarted")
    robot.set("rankedRoundFailed", tostring(reason or "start_match_failed"))
    clearBattleState()
end

local function markSharedFailed(reason)
    local teamKey = robot.get("rankedTeamKey")
    if teamKey then
        local hashKey = TEAM_PREFIX .. tostring(teamKey)
        share.hash_set(hashKey, "status", "failed", TEAM_TTL)
        share.hash_set(hashKey, "failReason", tostring(reason or "start_match_failed"), TEAM_TTL)
    end
end

local function cleanupStartMatchFailure(reason)
    markRoundNotStarted(reason)
    markSharedFailed(reason)
    leaveCurrentTeam(reason)
end

local function logStartMatchError(errObj, resp)
    local roleId = robot.get("roleId")
    local role = robot.get("rankedTeamRole")
    local teamId = robot.get("teamId")
    local teamKey = robot.get("rankedTeamKey")
    local targetSize = robot.get("rankedTeamTargetSize")
    local heroIds = robot.get("heroIdList")
    local currentHeroIds = robot.get("currentHeroIds")
    local teamMemberCount = robot.get("teamMemberCount")

    local code = errObj and errObj.code or nil
    local errDetail = errObj and errObj.detail or ""

    local detail = ""
    if code == 386 and resp ~= nil and type(resp) == "string" and #resp > 0 then
        local ok, errMsg = pcall(function()
            local errResp = proto.parse("Game.TeamStartMatchErrS2C", resp)
            local fields = proto.get_field_map(errResp)
            detail = " needHeroCnt=" .. tostring(fields.needHeroCnt)
                .. " needHeroLevel=" .. tostring(fields.needHeroLevel)
                .. " playerId=" .. listToString(fields.playerId)
                .. " errLadderPlayer=" .. listToString(fields.errLadderPlayer)
                .. " errCreditPlayer=" .. listToString(fields.errCreditPlayer)
                .. " errPenaltyPlayer=" .. mapToString(fields.errPenaltyPlayer)
                .. " errHeroPlayer=" .. mapToString(fields.errHeroPlayer)
        end)
        if not ok then
            detail = " parseErr=" .. tostring(errMsg) .. " rawLen=" .. tostring(#resp)
        end
    end

    log.warn("排位开始匹配失败: code=" .. tostring(code)
        .. " detail=" .. tostring(errDetail)
        .. " roleId=" .. tostring(roleId)
        .. " role=" .. tostring(role)
        .. " teamId=" .. tostring(teamId)
        .. " teamKey=" .. tostring(teamKey)
        .. " targetSize=" .. tostring(targetSize)
        .. " teamMemberCount=" .. tostring(teamMemberCount)
        .. " heroCount=" .. tostring(type(heroIds) == "table" and #heroIds or 0)
        .. " currentHeroIds=" .. listToString(currentHeroIds)
        .. detail)
end

function execute(r)
    robot.delete("rankedMatchStarted")

    local existingFailure = robot.get("rankedRoundFailed")
    if existingFailure then
        log.warn("排位开始匹配: 本轮已失败，跳过 StartMatch reason=" .. tostring(existingFailure))
        return nil
    end

    local role = robot.get("rankedTeamRole")
    local teamId = robot.get("teamId")

    -- 组队阶段可降级为单排；如果降级后没有 teamId，这里补建单排队伍。
    -- 补建只重试一次，仍失败才终止本轮，避免 314 无限刷屏。
    if (role == "solo" or not role) and not teamId then
        log.info("排位开始匹配: solo 缺少 teamId，补建单排队伍")
        local createMsg = proto.create("Game.TeamCreateC2S")
        proto.set_field(createMsg, "model", 2)
        local createErr, createResp = network.tcp_request("logic", {cmd=5, act=1}, createMsg, "Game.TeamCreateS2C")
        if createErr then
            log.warn("排位开始匹配: 补建队伍失败 code=" .. tostring(createErr.code) .. " detail=" .. tostring(createErr.detail) .. "，退出后重试一次")
            leaveCurrentTeam("start_create_retry_after_" .. tostring(createErr.code))
            createMsg = proto.create("Game.TeamCreateC2S")
            proto.set_field(createMsg, "model", 2)
            createErr, createResp = network.tcp_request("logic", {cmd=5, act=1}, createMsg, "Game.TeamCreateS2C")
            if createErr then
                log.warn("排位开始匹配: 补建队伍重试失败 code=" .. tostring(createErr.code) .. " detail=" .. tostring(createErr.detail) .. "，本轮跳过匹配")
                cleanupStartMatchFailure("start_create_failed_" .. tostring(createErr.code))
                return nil
            end
        end
        teamId = proto.get_field(createResp, "teamId")
        robot.set("teamId", teamId)
        robot.set("rankedTeamRole", "solo")
        robot.set("rankedTeamSize", 1)
        robot.set("rankedTeamTargetSize", 1)
    end

    local teamKey = robot.get("rankedTeamKey")

    -- 发送 TeamStartMatchC2S
    local msg = proto.create("Game.TeamStartMatchC2S")
    local err, resp = network.tcp_request("logic", {cmd=5, act=20}, msg, "Game.TeamStartMatchS2C")
    if err then
        logStartMatchError(err, resp)
        cleanupStartMatchFailure("start_failed_" .. tostring(err.code))
        return nil
    end

    robot.set("rankedMatchStarted", true)
    robot.delete("rankedRoundFailed")

    -- 更新 Redis 状态
    if teamKey then
        share.hash_set(TEAM_PREFIX .. teamKey, "status", "matching", TEAM_TTL)
        share.hash_set(TEAM_PREFIX .. teamKey, "updatedAtMs", tostring(utils.time_ms()), TEAM_TTL)
    end

    local modeId = proto.get_field(resp, "modeId")
    local predictCost = proto.get_field(resp, "predictCost")
    log.info("排位开始匹配成功: modeId=" .. tostring(modeId)
        .. " predictCost=" .. tostring(predictCost))
    return nil
end
