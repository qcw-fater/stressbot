-- 只校验奖励回执是否属于当前战斗。
local robot = require("robot")

function execute(r)
    local expected = robot.get("battleId")
    local actual = robot.get("sdcRewardBattleId")
    if expected and actual and tostring(expected) ~= tostring(actual) then
        return robot.error(54, "搜打撤奖励回执 battleId 不一致: expected="
            .. tostring(expected) .. " actual=" .. tostring(actual))
    end
    return nil
end
