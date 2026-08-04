-- 校验声明式 45:1 响应，并生成本局战前资产摘要。
local robot = require("robot")
local log = require("log")

local function valid_guid(value)
    return value ~= nil and tostring(value) ~= "" and tostring(value) ~= "0"
end

function execute(r)
    local errorCode = tonumber(robot.get("sdcPrepareErrorCode")) or 0
    if errorCode ~= 0 then
        return robot.error(errorCode, "搜打撤战备数据返回业务错误")
    end

    local data = robot.get("sdcPrepareData")
    if type(data) ~= "table" then
        return robot.error(54, "搜打撤战备数据缺少 data")
    end

    local guids = {}
    for _, slot in ipairs(data.equipSlots or {}) do
        if valid_guid(slot.itemGuid) then guids[#guids + 1] = slot.itemGuid end
    end
    for _, pack in ipairs(data.packSlots or {}) do
        for _, item in ipairs(pack.items or {}) do
            if valid_guid(item.guid) then guids[#guids + 1] = item.guid end
        end
    end

    robot.set("sdcInitialItemGuids", guids)
    robot.set("sdcPrepareTotalValue", tonumber(data.totalValue) or 0)
    log.info("搜打撤战备快照已生成: initialItems=" .. tostring(#guids)
        .. " totalValue=" .. tostring(data.totalValue))
    return nil
end
