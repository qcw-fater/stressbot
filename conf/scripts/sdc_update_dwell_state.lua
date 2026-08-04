-- 只负责判断本局随机停留是否结束。
local robot = require("robot")
local utils = require("utils")
local log = require("log")

function execute(r)
    local targetMs = tonumber(robot.get("sdcDwellTargetMs"))
    local startMs = tonumber(robot.get("sdcDwellStartMs"))
    if not targetMs or not startMs then return robot.error(54, "随机停留检查缺少计时状态") end
    local elapsed = utils.time_ms() - startMs
    if elapsed >= targetMs then
        robot.set("sdcDwellDone", true)
        log.info("搜打撤随机停留结束: elapsedMs=" .. tostring(elapsed)
            .. " targetMs=" .. tostring(targetMs) .. " battleId=" .. tostring(robot.get("battleId")))
    end
    return nil
end
