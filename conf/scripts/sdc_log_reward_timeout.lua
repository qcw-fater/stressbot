-- 只记录 4:72 奖励等待超时的本轮上下文，供并发压测关联服务器日志。
local robot = require("robot")
local log = require("log")

function execute(r)
    log.warn("搜打撤奖励等待超时上下文: battleId=" .. tostring(robot.get("battleId"))
        .. " outcome=" .. tostring(robot.get("sdcOutcome"))
        .. " fighterIndex=" .. tostring(robot.get("fighterIndex"))
        .. " teamId=" .. tostring(robot.get("teamId"))
        .. " frameCount=" .. tostring(robot.get("frameCount")))
    return nil
end
