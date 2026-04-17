-- battle_reward.lua: 等待战斗奖励（waitListen CMD=4, ACT=72 在 logic TCP 上）
local network = require("network")
local robot = require("robot")
local utils = require("utils")

function execute(r)
    -- 轮询监听战斗奖励消息，超时 60 秒
    local resp = network.wait_listen("logic", 4, 72, "Game.BattleRewardS2C", 60)
    if not resp then
        utils.log_error("BattleReward 超时")
        return 0  -- 不中断流程
    end

    utils.log_info("收到战斗奖励")
    return 0
end
