-- ranked_team_reset.lua: 每轮开始前清理上一轮排位状态
-- Reset 是长期压测的兜底恢复点：尽力退出服务端队伍，清掉本地状态，避免污染下一轮。
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local log = require("log")

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
        log.info("排位重置退出队伍: 本地无 teamId，跳过服务端退出")
        return
    end

    local msg = proto.create("Game.TeamLeaveC2S")
    proto.set_field(msg, "teamId", tonumber(teamId))
    local code = network.tcp_request("logic", {cmd=5, act=2}, msg, "Game.TeamLeaveS2C")
    log.info("排位重置退出队伍: teamId=" .. tostring(teamId) .. " code=" .. tostring(code))
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
    leaveTeamIfNeeded()
    clearLocalState()
    return 0
end
