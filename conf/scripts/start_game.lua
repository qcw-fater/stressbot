-- start_game.lua: 等待战斗开始（waitListen CMD=4, ACT=10 在 battle TCP 上）
local network = require("network")
local robot = require("robot")
local utils = require("utils")

function execute(r)
    -- 重置加载进度
    robot.set("loadProgress", 0)
    robot.set("packageIndex", 0)

    -- 轮询监听开始游戏消息，超时 60 秒
    local resp = network.wait_listen("battle", 4, 10, "Game.BattleStartGameS2C", 60)
    if not resp then
        utils.log_error("WaitStartGame 超时")
        return 1
    end

    utils.log_info("战斗开始")
    return 0
end
