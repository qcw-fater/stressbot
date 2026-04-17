-- check_role.lua: 检查是否有角色（boolean 脚本）
-- 返回 0 = true（有角色），1 = false（无角色）
local robot = require("robot")

function execute(r)
    local roles = robot.get("roles")
    if roles ~= nil and type(roles) == "table" and #roles > 0 then
        return 0  -- true: 有角色
    end
    return 1  -- false: 无角色
end
