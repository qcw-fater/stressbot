-- register_battle.lua: 注册到战斗服（BattleRegisterC2S CMD=4, ACT=1）
-- 需要先从 listen_start_loading 存储的 fighterIndex, battleId, battleSession
-- 战斗服可能还没准备好，加入重试机制
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    local fighterIndex = robot.get("fighterIndex")
    local battleId = robot.get("battleId") or 0
    local battleSession = robot.get("battleSession") or 0

    if not fighterIndex then
        utils.log_error("RegisterBattle: 缺少 fighterIndex")
        return 1
    end

    -- 战斗服可能还未创建好房间，重试最多 5 次，每次间隔 2 秒
    local maxRetry = 5
    for attempt = 1, maxRetry do
        local msg = proto.create("Game.BattleRegisterC2S")
        proto.set_field(msg, "index", fighterIndex)
        proto.set_field(msg, "battleId", battleId)
        proto.set_field(msg, "sessionId", battleSession)

        local code, resp = network.request("battle", 4, 1, msg, "Game.BattleRegisterS2C")
        if code == 0 then
            utils.log_info("RegisterBattle 成功: index=" .. tostring(fighterIndex)
                .. " battleId=" .. tostring(battleId))
            return 0
        end

        utils.log_info("RegisterBattle 第 " .. attempt .. " 次尝试失败: code=" .. tostring(code))
        if attempt < maxRetry then
            utils.log_info("等待 2 秒后重试...")
            utils.sleep(2000)
        end
    end

    utils.log_error("RegisterBattle 最终失败: 已重试 " .. maxRetry .. " 次")
    return 1
end
