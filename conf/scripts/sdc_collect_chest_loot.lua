-- 只负责从已选宝箱随机拾取 1~3 件可放入当前背包的道具。
local robot = require("robot")
local utils = require("utils")

local function valid_guid(value)
    return value ~= nil and tostring(value) ~= "" and tostring(value) ~= "0"
end

local function append_to_pack(packs, packType, item, value)
    for _, pack in ipairs(packs) do
        if tonumber(pack.packType) == tonumber(packType) then
            pack.items = pack.items or {}
            if #pack.items < (tonumber(pack.maxSlots) or 0) then
                item.containerType = 2
                item.containerDetail = packType
                pack.items[#pack.items + 1] = item
                pack.totalValue = (tonumber(pack.totalValue) or 0) + value
                return true
            end
        end
    end
    return false
end

function execute(r)
    local chest = robot.get("sdcSelectedChest") or {}
    local candidates = chest.eligible or {}
    local packs = robot.get("sdcBattlePackData") or {}
    local acquired = robot.get("sdcBattleAcquiredItems") or {}
    local acquiredGuids = {}
    for _, entry in ipairs(acquired) do
        if type(entry) == "table" and valid_guid(entry.guid) then
            acquiredGuids[tostring(entry.guid)] = true
        end
    end
    local desired = 1 + utils.random_int(3)
    local selected, addedValue = 0, 0

    while #candidates > 0 and selected < desired do
        local candidate = table.remove(candidates, utils.random_int(#candidates) + 1)
        local item = candidate.item or {}
        local ext = item.ext or {}
        local guid = item.guid
        local value = (tonumber(ext.value) or 0) * math.max(tonumber(ext.number) or 1, 1)
        if valid_guid(guid) and not acquiredGuids[tostring(guid)] then
            if append_to_pack(packs, candidate.packType, item, value) then
                acquiredGuids[tostring(guid)] = true
                selected = selected + 1
                addedValue = addedValue + value
                acquired[#acquired + 1] = {
                    guid = guid, itemId = tonumber(item.itemId) or 0,
                    packType = candidate.packType, value = value,
                }
            end
        end
    end

    robot.set("sdcBattlePackData", packs)
    robot.set("sdcBattleAcquiredItems", acquired)
    robot.set("sdcBattleLootValue", (tonumber(robot.get("sdcBattleLootValue")) or 0) + addedValue)
    robot.set("sdcChestRequestedLootCount", desired)
    robot.set("sdcChestSelectedLootCount", selected)
    robot.set("sdcChestAddedValue", addedValue)
    return nil
end
