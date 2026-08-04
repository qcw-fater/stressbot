-- 只记录一个已成功完成的战备动作，并决定是否结束本轮管理。
local robot = require("robot")
local log = require("log")

function execute(r)
    local kind = tostring(robot.get("sdcPrepareActionKind") or "")
    if kind == "" or kind == "none" then return nil end

    local completed = robot.get("sdcPrepareManagementActions") or {}
    local used = robot.get("sdcPrepareUsedKinds") or {}
    completed[#completed + 1] = kind
    used[kind] = true
    robot.set("sdcPrepareManagementActions", completed)
    robot.set("sdcPrepareUsedKinds", used)

    local target = tonumber(robot.get("sdcPrepareTargetCount")) or 1
    if #completed >= target then robot.set("sdcPrepareManagementDone", true) end
    log.info("搜打撤战备管理动作完成: action=" .. kind
        .. " completed=" .. tostring(#completed) .. "/" .. tostring(target))
    return nil
end
