-- sdc_check_team_cleared.lua: 将本地 teamId 是否已清除转换为显式布尔状态。
-- 只负责状态判断；退队请求和恢复分支均由 flow 节点编排。
local robot = require("robot")

function execute(r)
    local teamId = robot.get("teamId")
    local cleared = teamId == nil or teamId == 0 or teamId == "0"
    robot.set("sdcTeamCleared", cleared)
    return nil
end
