-- 校验声明式批量出售请求是否出售了全部选中的道具。
local robot = require("robot")
local log = require("log")

function execute(r)
    local code = tonumber(robot.get("sdcOutBattleSellErrorCode")) or 0
    if code ~= 0 then return robot.error(code, "搜打撤随机出售返回业务错误") end
    local requested = robot.get("sdcOutBattleRandomSellGuid") or {}
    local sold = robot.get("sdcOutBattleSoldGuids") or {}
    if #sold ~= #requested then
        return robot.error(54, "搜打撤随机出售数量不一致: requested=" .. tostring(#requested)
            .. " sold=" .. tostring(#sold))
    end
    log.info("搜打撤随机出售完成: count=" .. tostring(#sold))
    return nil
end
