-- 只负责从已选宝箱随机拾取 1~3 件可放入当前背包的道具。
local robot = require("robot")
local share = require("share")
local utils = require("utils")

local CLAIM_TTL_SECONDS = 900

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
    local battleId = robot.get("battleId")
    local owner = tostring(robot.get("roleId") or robot.get("playerId") or "")
    if #candidates > 0 and (not valid_guid(battleId) or owner == "" or owner == "0") then
        return robot.error(54, "局内战利品占用缺少 battleId 或 playerId")
    end
    local desired = 1 + utils.random_int(3)
    local selected, addedValue = 0, 0

    while #candidates > 0 and selected < desired do
        local candidate = table.remove(candidates, utils.random_int(#candidates) + 1)
        local item = candidate.item or {}
        local ext = item.ext or {}
        local guid = item.guid
        local value = (tonumber(ext.value) or 0) * math.max(tonumber(ext.number) or 1, 1)
        if valid_guid(guid) then
            local claimKey = "sdc:loot:" .. tostring(battleId) .. ":" .. tostring(guid)
            local claimed, claimErr = share.claim(claimKey, owner, CLAIM_TTL_SECONDS)
            if claimErr ~= nil then
                return robot.error(54, "局内战利品占用失败: " .. tostring(claimErr))
            end
            if claimed then
                if append_to_pack(packs, candidate.packType, item, value) then
                    selected = selected + 1
                    addedValue = addedValue + value
                    acquired[#acquired + 1] = {
                        guid = guid, itemId = tonumber(item.itemId) or 0,
                        packType = candidate.packType, value = value,
                    }
                else
                    local _, releaseErr = share.release(claimKey, owner)
                    if releaseErr ~= nil then
                        return robot.error(54, "释放未拾取战利品占用失败: " .. tostring(releaseErr))
                    end
                end
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
