-- check_ready.lua：lua: 条件脚本（用于节点条件 "lua:check_ready.lua"）
-- 契约：必须 return 真 boolean；返回其他类型视为错误，条件按 false。
local robot = require("robot")

function execute(r)
    -- 背包数据已就绪且等级 >= 5 才进入购买
    local level = robot.get_path("playerLevel")
    if level == nil then
        return false
    end
    return level >= 5
end
