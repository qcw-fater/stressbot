-- normal_check_battle_reconnection.lua: use the server's official recovery route before normal matchmaking.
-- Error 310 means the referenced team no longer exists, so the desired clean state has already been reached.
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

local function clearTeamState()
    robot.delete("normalBattleReconnection")
    robot.delete("teamId")
    robot.delete("teamData")
    robot.delete("teamMemberCount")
    robot.delete("teamHeaderId")
end

function execute(r)
    local msg = proto.create("Game.BattleCheckReconnectionC2S")
    local err, resp = network.tcp_request("logic", {cmd=4, act=19}, msg,
        "Game.BattleCheckReconnectionS2C", 60)

    if err ~= nil then
        if tonumber(err.code) == 310 then
            clearTeamState()
            robot.set("normalStartupRecoveryComplete", true)
            robot.set("normalRecoveryCanLeaveTeam", false)
            log.info("普通模式重连检查确认服务端已无队伍，按幂等成功继续")
            return nil
        end
        return err
    end

    if type(resp) == "string" and #resp == 0 then
        clearTeamState()
        robot.set("normalStartupRecoveryComplete", true)
        robot.set("normalRecoveryCanLeaveTeam", false)
        log.info("普通模式重连检查返回空上下文，确认服务端无队伍或战斗残留")
        return nil
    end
    if type(resp) ~= "userdata" then
        return robot.error(54, "普通模式重连检查响应形态异常: " .. type(resp))
    end

    robot.set("normalBattleReconnection", resp)
    return nil
end
