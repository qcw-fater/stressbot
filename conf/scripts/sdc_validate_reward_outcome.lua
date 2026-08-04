-- 只校验奖励回执的结局；战利品是否真正落地由后续服务端战备/仓库快照校验。
local robot = require("robot")
local log = require("log")

function execute(r)
    local expected = tonumber(robot.get("sdcExpectedSettleResult")) or 1
    local result = tonumber(robot.get("sdcRewardResult")) or 0
    local lootValue = tonumber(robot.get("sdcRewardLootValue")) or 0
    if result ~= expected then
        return robot.error(54, "搜打撤奖励结局不一致: expected="
            .. tostring(expected) .. " actual=" .. tostring(result))
    end
    local settle = robot.get("sdcRewardSettleData") or {}
    robot.set("sdcRewardKeptCount", #(settle.keptItemGuids or {}))
    robot.set("sdcRewardAcquiredCount", #(settle.acquiredItemIds or {}))
    log.info("搜打撤奖励回执已确认: outcome=" .. tostring(robot.get("sdcOutcome"))
        .. " result=" .. tostring(result) .. " lootValue=" .. tostring(lootValue))
    return nil
end
