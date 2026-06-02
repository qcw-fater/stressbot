-- check_role.lua: 检查是否有角色（boolean 脚本）
-- 必须 return true/false（条件节点 / loop breakCondition 由 RunBooleanScript 调用，
-- 旧的 0/1 数字写法已废弃，详见 script.RunBooleanScript 注释）。
local robot = require("robot")
local log = require("log")

function execute(r)
    local roles = robot.get("roles")
    if roles ~= nil and type(roles) == "table" and #roles > 0 then
        robot.set("isNewRole", false)
        log.debug("检查角色: roles=" .. tostring(#roles) .. " isNewRole=false")
        return true
    end
    robot.set("isNewRole", true)
    local roleType = type(roles)
    local roleCount = 0
    if roles ~= nil and type(roles) == "table" then
        roleCount = #roles
    end
    log.debug("检查角色: roles=" .. tostring(roleCount)
        .. " rolesType=" .. tostring(roleType)
        .. " isNewRole=true")
    return false
end
