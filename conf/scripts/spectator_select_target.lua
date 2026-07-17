-- spectator_select_target.lua: 从 31|1 大神榜回包里随机选一场可观战对局，写入 obTargetBattleId/obTargetModeId
-- 空榜（无在战对局 / modeId 不匹配）→ 写 0，由流程的 hasTarget 判定跳过本轮、下轮重试。
-- list 元素结构：ObBattleClient { base: ObBattleBaseClient { battleId, battleModeId, model, ... }, playerCount }
local robot = require("robot")
local utils = require("utils")
local log = require("log")

function execute(r)
    local list = robot.get("obBattleList")
    -- obHasTarget 为 int(1/0) 供条件节点判断（battleId 是雪花大整数，Lua 里是字符串，
    -- 不能直接与数字比较，故用独立 int 标志位，避免条件类型不匹配）。
    robot.set("obHasTarget", 0)
    robot.set("obTargetBattleId", 0)
    robot.set("obTargetModeId", 0)
    robot.set("obPlayerCount", 0)

    if type(list) ~= "table" then
        return nil
    end

    local cands = {}
    for _, item in ipairs(list) do
        if type(item) == "table" and type(item.base) == "table" then
            local bid = item.base.battleId
            -- battleId 为字符串（int64 雪花），非空字符串即有效
            if bid ~= nil and bid ~= "" and bid ~= 0 then
                table.insert(cands, item)
            end
        end
    end

    if #cands == 0 then
        log.info("观战榜暂无可观战对局，本轮跳过待重试")
        return nil
    end

    local pick = utils.random_pick(cands)
    robot.set("obHasTarget", 1)
    robot.set("obTargetBattleId", pick.base.battleId)
    robot.set("obTargetModeId", pick.base.battleModeId or 0)
    robot.set("obPlayerCount", pick.playerCount or 0)
    log.info("选观战目标: battleId=" .. tostring(pick.base.battleId)
        .. " modeId=" .. tostring(pick.base.battleModeId)
        .. " 观看人数=" .. tostring(pick.playerCount))
    return nil
end
