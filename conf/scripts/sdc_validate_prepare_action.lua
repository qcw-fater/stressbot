-- 只校验一次声明式战备操作的业务错误码。
local robot = require("robot")

function execute(r)
    local code = tonumber(robot.get("sdcPrepareActionErrorCode")) or 0
    if code ~= 0 then
        return robot.error(code, "搜打撤战备操作返回业务错误: action="
            .. tostring(robot.get("sdcPrepareActionKind")))
    end
    return nil
end
