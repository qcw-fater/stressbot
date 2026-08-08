-- normal_parse_battle_reconnection.lua: 判定官方重连检查是否已经把服务端残留清理干净。
-- teamId/roomId/battleId 均可能超过 Lua number 的安全整数范围，禁止 tonumber。
local robot = require("robot")
local proto = require("proto")
local log = require("log")

local function hasId(value)
    return value ~= nil and value ~= 0 and value ~= "0" and value ~= ""
end

function execute(r)
    local reconnection = robot.get_view("normalBattleReconnection")
    if reconnection == nil then
        if robot.get("normalStartupRecoveryComplete") == true then
            return nil
        end
        return robot.error(54, "普通模式重连检查响应不存在")
    end

    local teamId = proto.get_path(reconnection, "teamData.teamId")
    local roomId = proto.get_path(reconnection, "roomData.roomId")
    local battleId = proto.get_field(reconnection, "battleId")
    local hasTeam = hasId(teamId)
    local hasRoom = hasId(roomId)
    local hasBattle = hasId(battleId)
    local active = hasTeam or hasRoom or hasBattle

    robot.set("normalRecoveryCanLeaveTeam", hasTeam and not hasRoom and not hasBattle)
    robot.set("normalStartupRecoveryComplete", not active)

    if hasTeam then
        robot.set("teamId", teamId)
    end

    if active then
        log.warn("普通模式登录发现真实队伍/战斗上下文，等待服务端恢复: teamId="
            .. tostring(teamId) .. " roomId=" .. tostring(roomId)
            .. " battleId=" .. tostring(battleId))
    else
        robot.delete("normalBattleReconnection")
        robot.delete("teamId")
        log.info("普通模式登录残留已由服务端重连检查清理")
    end
    return nil
end
