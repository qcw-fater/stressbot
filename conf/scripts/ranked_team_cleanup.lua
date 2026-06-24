-- ranked_team_cleanup.lua: 每轮结束后清理排位组队状态
-- Cleanup 是正常出口；异常/跳过仍依赖下一轮 reset 做兜底恢复。
local share = require("share")
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local log = require("log")

local TEAM_PREFIX = "ranked:v2:team:"

local function currentTeamId()
    local teamId = robot.get("teamId")
    if teamId then return teamId end

    local teamData = robot.get("teamData")
    if type(teamData) == "table" then
        if teamData.id then return teamData.id end
        if type(teamData.teamData) == "table" and teamData.teamData.id then
            return teamData.teamData.id
        end
    end
    return nil
end

local function leaveTeamIfNeeded()
    local teamId = currentTeamId()
    if not teamId then
        log.info("排位清理退出队伍: 本地无 teamId，跳过服务端退出")
        return
    end

    local msg = proto.create("Game.TeamLeaveC2S")
    proto.set_field(msg, "teamId", tonumber(teamId))
    local err = network.tcp_request("logic", {cmd=5, act=2}, msg, "Game.TeamLeaveS2C")
    local codeText = err and (tostring(err.code) .. " " .. tostring(err.detail)) or "0"
    log.info("排位清理退出队伍: teamId=" .. tostring(teamId) .. " code=" .. codeText)
end

local function clearLocalState()
    local keys = {
        "rankedTeamSize", "rankedTeamRole", "rankedTeamKey", "rankedTeamInvite",
        "rankedTeamInviteReceived", "rankedTeamAcceptDone", "rankedTeamReady",
        "rankedTeamMembers", "rankedTeamLeaderId", "rankedTeamTargetSize",
        "rankedTeamDesiredSize", "rankedQueueToken", "teamId", "teamJoinCode",
        "teamModel", "teamGType", "teamModeId", "teamTsId", "teamData",
        "teamMemberCount", "teamHeaderId", "rankedMatchStarted", "rankedRoundFailed",
        "battleId", "battleAddress", "battleSecretKey", "battleSession", "battleArea",
        "battleGameType", "fighterIndex", "fighterListData", "packageIndex",
        "battleAck", "loadProgress",
    }
    for _, k in ipairs(keys) do
        robot.delete(k)
    end
end

function execute(r)
    local teamKey = robot.get("rankedTeamKey")
    local role = robot.get("rankedTeamRole")

    leaveTeamIfNeeded()

    if teamKey and (role == "solo" or role == "leader") then
        local _, err = share.hash_set(TEAM_PREFIX .. tostring(teamKey), "status", "done", 600)
        if err then
            log.info("排位清理: 更新队伍状态失败（可忽略）: " .. tostring(err))
        end
    end

    clearLocalState()
    log.info("排位组队清理完成")
    return nil
end
