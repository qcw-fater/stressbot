-- sdc_leave_team.lua: 搜打撤单排退队（每轮开头、结尾和异常恢复共用）。
-- SDC 是单排自建队，机器人永远是队长，因此遇到匹配状态(288/311)时必须先取消匹配再退队，
-- 不能像排位队员那样保留 teamId 等队长清理——否则匹配卡住后会永久卡在恢复循环。
-- teamId 是 int64 雪花 ID（可能 >2^53），必须原样透传给 proto.set_field，禁止 tonumber。
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

local function currentTeamId()
    local teamId = robot.get("teamId")
    if teamId and teamId ~= 0 and teamId ~= "0" then
        return teamId
    end

    local teamData = robot.get("teamData")
    if type(teamData) == "table" then
        if teamData.id and teamData.id ~= 0 and teamData.id ~= "0" then
            return teamData.id
        end
        local nested = teamData.teamData
        if type(nested) == "table" and nested.id and nested.id ~= 0 and nested.id ~= "0" then
            return nested.id
        end
    end
    return nil
end

local function clearTeamState()
    robot.delete("teamId")
    robot.delete("teamData")
    robot.delete("teamMemberCount")
    robot.delete("teamHeaderId")
end

local function leaveOnce(teamId)
    local msg = proto.create("Game.TeamLeaveC2S")
    proto.set_field(msg, "teamId", teamId)
    return network.tcp_request("logic", {cmd=5, act=2}, msg, "Game.TeamLeaveS2C")
end

local function cancelMatch()
    local msg = proto.create("Game.TeamCancelMatchC2S")
    return network.tcp_request("logic", {cmd=5, act=21}, msg, "Game.TeamCancelMatchS2C")
end

function execute(r)
    local teamId = currentTeamId()
    if not teamId then
        return nil
    end

    local err = leaveOnce(teamId)
    if err == nil then
        clearTeamState()
        log.info("搜打撤退队成功: teamId=" .. tostring(teamId))
        return nil
    end

    local code = tonumber(err.code)
    -- logic 会在请求到达 team 服务前检查玩家状态：PS_Matching 返回 288；
    -- 若请求进入 team 服务后才发现正在匹配，则返回 311。两者都需要先取消匹配。
    -- SDC 单排机器人是队长，必须主动取消匹配，否则匹配卡住后服务端保留僵尸队伍。
    if code == 311 or code == 288 then
        local cancelErr = cancelMatch()
        if cancelErr ~= nil then
            log.warn("搜打撤取消匹配失败，继续重试退队: teamId=" .. tostring(teamId)
                .. " code=" .. tostring(cancelErr.code))
        end

        local err2 = leaveOnce(teamId)
        if err2 == nil then
            clearTeamState()
            log.info("搜打撤取消匹配后退队成功: teamId=" .. tostring(teamId))
            return nil
        end
        if tonumber(err2.code) == 308 then
            clearTeamState()
            log.warn("搜打撤退队时服务端已无该队伍，清理本地状态: teamId=" .. tostring(teamId))
            return nil
        end
        log.warn("搜打撤取消匹配后退队仍失败，保留 teamId: teamId=" .. tostring(teamId)
            .. " code=" .. tostring(err2.code) .. " detail=" .. tostring(err2.detail))
        return err2
    end

    if code == 308 then
        clearTeamState()
        log.warn("搜打撤退队时服务端已无该队伍，清理本地状态: teamId=" .. tostring(teamId))
        return nil
    end

    log.warn("搜打撤退队失败，保留 teamId: teamId=" .. tostring(teamId)
        .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
    return err
end
