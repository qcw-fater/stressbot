-- 只校验声明式批量出售请求是否恰好出售了选中的一件道具。
local robot = require("robot")
local log = require("log")

function execute(r)
    local code = tonumber(robot.get("sdcOutBattleSellErrorCode")) or 0
    if code ~= 0 then return robot.error(code, "搜打撤随机出售返回业务错误") end
    local requested = robot.get("sdcOutBattleRandomSellGuid")
    local sold = robot.get("sdcOutBattleSoldGuids") or {}
    if #sold ~= 1 or tostring(sold[1]) ~= tostring(requested) then
        return robot.error(54, "搜打撤随机出售结果不一致: requested="
            .. tostring(requested) .. " sold=" .. tostring(sold[1]))
    end
    log.info("搜打撤随机出售完成: guid=" .. tostring(requested))
    return nil
end
