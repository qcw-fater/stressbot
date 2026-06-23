-- ranked_leader_wait_ready.lua: 队长等待所有成员就绪
-- 仅队长角色执行。先发送 TeamPrepareC2S 通知服务端，再标记 Redis 就绪，然后轮询 Redis ready 数。
-- 成功时保持 leader 多排；失败/超时时降级单排继续，避免组队不稳定挡住开始战斗。
local share = require("share")
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local utils = require("utils")
local log = require("log")

local TEAM_TTL = 600     -- TEAM_TTL: 所有排位脚本统一 600s
local TIMEOUT_MS = 60000 -- 等成员就绪 60s，给多排协作足够时间

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
        log.info("排位队长等就绪清理队伍: reason=" .. tostring(reason)
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
    log.warn("排位队长等就绪: " .. tostring(reason) .. "，降级单排继续")
end

function execute(r)
    local teamKey = robot.get("rankedTeamKey")
    if not teamKey then
        degradeSolo("teamKey 为空")
        return nil
    end

    local roleId = tostring(robot.get("roleId"))
    local targetSize = tonumber(robot.get("rankedTeamTargetSize") or robot.get("rankedTeamSize") or 1)

    -- 通知服务端自己已准备（必须在 Redis 标记之前）
    local msg = proto.create("Game.TeamPrepareC2S")
    proto.set_field(msg, "isPrepare", true)
    local err = network.tcp_send("logic", {cmd=5, act=12}, msg)
    if err then
        share.hash_set("ranked:v1:team:" .. teamKey, "status", "done", TEAM_TTL)
        share.hash_set("ranked:v1:team:" .. teamKey, "failReason", "leader_prepare_send_failed_" .. tostring(err.code), TEAM_TTL)
        degradeSolo("TeamPrepare 发送失败 code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return nil
    else
        log.info("排位队长等就绪: TeamPrepare 已发送")
    end

    -- 标记自己就绪（Redis）
    local readyKey = "ranked:v1:team:" .. teamKey .. ":ready"
    share.hash_set(readyKey, roleId, "1", TEAM_TTL)

    -- 轮询等待所有成员就绪
    local waited = 0
    while waited < TIMEOUT_MS do
        local allReady, merr = share.hash_get_all(readyKey)
        if not merr and allReady then
            local count = 0
            for _, _ in pairs(allReady) do
                count = count + 1
            end
            if count >= targetSize then
                -- 同时检查服务端队伍人数
                local serverCount = tonumber(robot.get("teamMemberCount") or "0") or 0
                if serverCount >= targetSize or targetSize <= 1 then
                    share.hash_set("ranked:v1:team:" .. teamKey, "status", "ready", TEAM_TTL)
                    robot.delete("rankedRoundFailed")
                    log.info("排位队长等就绪: 完成 count=" .. tostring(count)
                        .. "/" .. tostring(targetSize))
                    return nil
                end
            end
        end
        utils.sleep(1000)
        waited = waited + 1000
    end

    share.hash_set("ranked:v1:team:" .. teamKey, "status", "done", TEAM_TTL)
    share.hash_set("ranked:v1:team:" .. teamKey, "failReason", "leader_ready_timeout", TEAM_TTL)
    degradeSolo("超时 targetSize=" .. tostring(targetSize))
    return nil
end
