-- 只基于声明式战备/仓库快照校验出售和入库结果。
local robot = require("robot")
local log = require("log")

local function valid_guid(value)
    return value ~= nil and tostring(value) ~= "" and tostring(value) ~= "0"
end

local function collect_prepare(data)
    local result = {}
    for _, slot in ipairs(data.equipSlots or {}) do
        if valid_guid(slot.itemGuid) then result[tostring(slot.itemGuid)] = true end
    end
    for _, pack in ipairs(data.packSlots or {}) do
        for _, item in ipairs(pack.items or {}) do
            if valid_guid(item.guid) then result[tostring(item.guid)] = true end
        end
    end
    return result
end

function execute(r)
    local prepareCode = tonumber(robot.get("sdcVerifyPrepareErrorCode")) or 0
    local warehouseCode = tonumber(robot.get("sdcVerifyWarehouseErrorCode")) or 0
    if prepareCode ~= 0 then return robot.error(prepareCode, "局外校验战备返回业务错误") end
    if warehouseCode ~= 0 then return robot.error(warehouseCode, "局外校验仓库返回业务错误") end
    local prepare = robot.get("sdcVerifyPrepareData")
    local warehouse = robot.get("sdcVerifyWarehouseData")
    if type(prepare) ~= "table" or type(warehouse) ~= "table" then
        return robot.error(54, "局外资产校验缺少声明式快照")
    end

    local prepareGuids = collect_prepare(prepare)
    local warehouseGuids = {}
    for _, item in ipairs(warehouse.items or {}) do
        if valid_guid(item.guid) then warehouseGuids[tostring(item.guid)] = true end
    end
    for _, guid in ipairs(robot.get("sdcOutBattleSoldGuids") or {}) do
        if prepareGuids[tostring(guid)] or warehouseGuids[tostring(guid)] then
            return robot.error(54, "已出售道具仍存在: guid=" .. tostring(guid))
        end
    end
    for _, guid in ipairs(robot.get("sdcOutBattleDepositedGuids") or {}) do
        if not warehouseGuids[tostring(guid)] then
            return robot.error(54, "入库道具未出现在仓库: guid=" .. tostring(guid))
        end
        if prepareGuids[tostring(guid)] then
            return robot.error(54, "入库道具仍残留在战备: guid=" .. tostring(guid))
        end
    end
    log.info("搜打撤局外资产校验完成: sold="
        .. tostring(#(robot.get("sdcOutBattleSoldGuids") or {}))
        .. " deposited=" .. tostring(#(robot.get("sdcOutBattleDepositedGuids") or {})))
    return nil
end
