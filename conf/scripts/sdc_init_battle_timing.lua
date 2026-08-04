-- 只负责生成局内起始时间与首次开箱时刻。
local robot = require("robot")
local utils = require("utils")

function execute(r)
    local targetMs = tonumber(robot.get("sdcDwellTargetMs"))
    local openTarget = tonumber(robot.get("sdcChestOpenTarget"))
    if not targetMs or not openTarget or openTarget < 1 then
        return robot.error(54, "搜打撤局内计时初始化参数无效")
    end
    robot.set("sdcDwellStartMs", utils.time_ms())
    robot.set("sdcNextChestOpenAtMs", math.max(500, math.floor(targetMs / (openTarget + 1))))
    return nil
end
