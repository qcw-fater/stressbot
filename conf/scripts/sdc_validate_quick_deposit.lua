-- 只校验并记录声明式一键入库响应。
local robot = require("robot")
local log = require("log")

function execute(r)
    local code = tonumber(robot.get("sdcOutBattleDepositErrorCode")) or 0
    if code ~= 0 then return robot.error(code, "搜打撤一键入库返回业务错误") end
    local deposited = robot.get("sdcOutBattleDepositedGuids") or {}
    log.info("搜打撤一键入库完成: deposited=" .. tostring(#deposited)
        .. " totalValue=" .. tostring(robot.get("sdcOutBattleDepositTotalValue")))
    return nil
end
