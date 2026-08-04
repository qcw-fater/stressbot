-- 只基于声明式战备/仓库快照校验不同结局下资产保留、丢失及钥匙消耗。
local robot = require("robot")
local log = require("log")

local function valid_guid(value)
    return value ~= nil and tostring(value) ~= "" and tostring(value) ~= "0"
end

local function collect(data, result)
    for _, slot in ipairs(data.equipSlots or {}) do
        if valid_guid(slot.itemGuid) then result[tostring(slot.itemGuid)] = slot end
    end
    for _, pack in ipairs(data.packSlots or {}) do
        for _, item in ipairs(pack.items or {}) do
            if valid_guid(item.guid) then result[tostring(item.guid)] = item end
        end
    end
end

function execute(r)
    local prepareCode = tonumber(robot.get("sdcVerifyPrepareErrorCode")) or 0
    local warehouseCode = tonumber(robot.get("sdcVerifyWarehouseErrorCode")) or 0
    if prepareCode ~= 0 then return robot.error(prepareCode, "结局校验战备返回业务错误") end
    if warehouseCode ~= 0 then return robot.error(warehouseCode, "结局校验仓库返回业务错误") end
    local prepare = robot.get("sdcVerifyPrepareData")
    local warehouse = robot.get("sdcVerifyWarehouseData")
    if type(prepare) ~= "table" or type(warehouse) ~= "table" then
        return robot.error(54, "结局资产校验缺少声明式快照")
    end

    local actual = {}
    collect(prepare, actual)
    for _, item in ipairs(warehouse.items or {}) do
        if valid_guid(item.guid) then actual[tostring(item.guid)] = item end
    end
    local lost = robot.get("sdcExpectedLostGuids") or {}
    local kept = robot.get("sdcExpectedKeptGuids") or {}
    for _, guid in ipairs(lost) do
        if actual[tostring(guid)] then
            return robot.error(54, "结局应丢失资产仍存在: guid=" .. tostring(guid))
        end
    end
    for _, guid in ipairs(kept) do
        if not actual[tostring(guid)] then
            return robot.error(54, "结局应保留资产已丢失: guid=" .. tostring(guid))
        end
    end
    for _, record in ipairs(robot.get("sdcKeyUseRecords") or {}) do
        local item = actual[tostring(record.guid)]
        if not item then return robot.error(54, "开箱钥匙未保留: guid=" .. tostring(record.guid)) end
        local ext = item.ext or {}
        if record.mode == "number" and tonumber(ext.number) ~= tonumber(record.afterNumber) then
            return robot.error(54, "钥匙堆叠消耗未生效: guid=" .. tostring(record.guid))
        end
        if record.mode == "durability"
            and tonumber(ext.durability) ~= tonumber(record.afterDurability) then
            return robot.error(54, "钥匙耐久消耗未生效: guid=" .. tostring(record.guid))
        end
    end
    log.info("搜打撤结局资产校验完成: lost=" .. tostring(#lost) .. " kept=" .. tostring(#kept))
    return nil
end
