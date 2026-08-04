-- 只负责提交一次开箱事件记录并安排下一次开箱时刻。
local robot = require("robot")
local log = require("log")

function execute(r)
    local chest = robot.get("sdcSelectedChest") or {}
    local openCount = (tonumber(robot.get("sdcChestOpenCount")) or 0) + 1
    local openTarget = tonumber(robot.get("sdcChestOpenTarget")) or 1
    local elapsed = tonumber(robot.get("sdcChestElapsedMs")) or 0
    local opened = robot.get("sdcOpenedChestKeys") or {}
    local events = robot.get("sdcChestOpenEvents") or {}

    opened[tostring(chest.sourceKey)] = true
    events[#events + 1] = {
        index = openCount, sourceKey = chest.sourceKey, chestId = chest.chestId,
        chestType = chest.chestType,
        requested = tonumber(robot.get("sdcChestRequestedLootCount")) or 0,
        selected = tonumber(robot.get("sdcChestSelectedLootCount")) or 0,
        addedValue = tonumber(robot.get("sdcChestAddedValue")) or 0,
        hasPlayerExtra = chest.hasPlayerExtra == true,
        keyUseMode = tostring(robot.get("sdcCurrentKeyUseMode") or "none"),
    }
    robot.set("sdcOpenedChestKeys", opened)
    robot.set("sdcChestOpenEvents", events)
    robot.set("sdcLootChestId", tonumber(chest.chestId) or 0)
    robot.set("sdcChestOpenCount", openCount)
    robot.set("sdcChestSelected", false)

    if openCount >= openTarget then
        robot.set("sdcLootEventDone", true)
    else
        local targetMs = tonumber(robot.get("sdcDwellTargetMs")) or elapsed
        robot.set("sdcNextChestOpenAtMs",
            math.max(elapsed + 250, math.floor(targetMs * (openCount + 1) / (openTarget + 1))))
    end
    log.info("搜打撤开箱完成: opened=" .. tostring(openCount) .. "/" .. tostring(openTarget)
        .. " source=" .. tostring(chest.sourceKey) .. " chestId=" .. tostring(chest.chestId)
        .. " selected=" .. tostring(robot.get("sdcChestSelectedLootCount"))
        .. " addedValue=" .. tostring(robot.get("sdcChestAddedValue")))
    return nil
end
