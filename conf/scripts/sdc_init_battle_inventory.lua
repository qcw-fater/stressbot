-- 只负责从战前快照复制本局背包与装配状态。
local robot = require("robot")
local log = require("log")

function execute(r)
    local prepare = robot.get("sdcPrepareData")
    local snapshotWire = robot.get("sdcLoadingSnapshotWire")
    if type(prepare) ~= "table" then
        return robot.error(54, "搜打撤局内库存初始化缺少战前快照")
    end
    if type(snapshotWire) ~= "string" or #snapshotWire == 0 then
        return robot.error(54, "搜打撤局内库存初始化缺少 Loading 快照")
    end
    robot.set("sdcBattlePackData", prepare.packSlots or {})
    robot.set("sdcBattleEquipData", prepare.equipSlots or {})
    log.info("搜打撤局内库存已初始化: battleId=" .. tostring(robot.get("battleId"))
        .. " targetMs=" .. tostring(robot.get("sdcDwellTargetMs"))
        .. " chestTarget=" .. tostring(robot.get("sdcChestOpenTarget")))
    return nil
end
