-- 只负责钥匙箱对应的一次钥匙链消耗。
local robot = require("robot")
local utils = require("utils")

local function valid_guid(value)
    return value ~= nil and tostring(value) ~= "" and tostring(value) ~= "0"
end

function execute(r)
    local chest = robot.get("sdcSelectedChest") or {}
    robot.set("sdcCurrentKeyUseMode", "none")
    if tonumber(chest.chestType) ~= 1 then return nil end

    local packs = robot.get("sdcBattlePackData") or {}
    local keys = {}
    for _, pack in ipairs(packs) do
        if tonumber(pack.packType) == 4 then
            for _, item in ipairs(pack.items or {}) do
                if valid_guid(item.guid) and tonumber((item.ext or {}).sdcType) == 6 then
                    keys[#keys + 1] = item
                end
            end
        end
    end
    if #keys == 0 then return robot.error(54, "钥匙箱开箱时钥匙链已无可用钥匙") end

    local item = keys[utils.random_int(#keys) + 1]
    item.ext = item.ext or {}
    local beforeNumber = tonumber(item.ext.number) or 0
    local beforeDurability = tonumber(item.ext.durability) or 0
    local mode = "retained_single"
    if beforeNumber > 1 then
        item.ext.number = beforeNumber - 1
        mode = "number"
    elseif beforeDurability > 1 then
        item.ext.durability = beforeDurability - 1
        mode = "durability"
    end
    local records = robot.get("sdcKeyUseRecords") or {}
    records[#records + 1] = {
        guid = item.guid, itemId = item.itemId, mode = mode, exactKeyMatch = false,
        beforeNumber = beforeNumber, afterNumber = tonumber(item.ext.number) or beforeNumber,
        beforeDurability = beforeDurability,
        afterDurability = tonumber(item.ext.durability) or beforeDurability,
        chestKey = chest.sourceKey, chestId = chest.chestId,
    }
    robot.set("sdcBattlePackData", packs)
    robot.set("sdcKeyUseRecords", records)
    robot.set("sdcCurrentKeyUseMode", mode)
    return nil
end
