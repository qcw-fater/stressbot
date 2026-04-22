-- game_over.lua: 发送游戏结束（MainGameOverC2S CMD=2, ACT=62）
-- proto: operation(BattleGameOver enum), BGO_BackLobby=0, BGO_BackTeam=1, BGO_BackRoom=2
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    local msg = proto.create("Game.MainGameOverC2S")
    proto.set_field(msg, "operation", 0)  -- BGO_BackLobby 返回大厅

    local code, resp = network.request("logic", {cmd=2, act=62}, msg, "Game.MainGameOverS2C")
    if code ~= 0 then
        utils.log_error("GameOver 失败: code=" .. tostring(code))
    end

    -- 清理战斗相关连接与状态，确保下轮 businessLoop 可重新匹配
    network.close_udp()
    network.close_tcp("battle")
    robot.clear("battleId")
    robot.clear("battleAddress")
    robot.clear("battleSecretKey")
    robot.clear("battleSession")
    robot.clear("fighterIndex")
    robot.clear("fighterListData")
    robot.clear("packageIndex")
    robot.clear("battleAck")

    utils.log_info("GameOver 已发送，战斗连接已清理")
    return 0
end
