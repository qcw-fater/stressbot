-- 只负责从四份声明式服务端快照中选择一个当前合法且未执行过的战备动作。
local robot = require("robot")
local utils = require("utils")

local function valid_guid(value)
    return value ~= nil and tostring(value) ~= "" and tostring(value) ~= "0"
end

local function check_response(codeKey, label)
    local code = tonumber(robot.get(codeKey)) or 0
    if code ~= 0 then return robot.error(code, label .. "返回业务错误") end
    return nil
end

local function pack_type_for_item(item)
    local itemType = tonumber(item and item.ext and item.ext.sdcType) or 0
    if itemType == 3 then return 1 end
    if itemType == 4 then return 5 end
    if itemType == 5 then return 2 end
    if itemType == 6 then return 4 end
    return nil
end

local function pack_map(prepare)
    local result = {}
    for _, pack in ipairs(prepare.packSlots or {}) do
        result[tonumber(pack.packType) or 0] = pack
    end
    return result
end

local function occupied_slots(prepare)
    local result = {}
    for _, slot in ipairs(prepare.equipSlots or {}) do
        if valid_guid(slot.itemGuid) then result[tonumber(slot.slotType) or 0] = slot end
    end
    return result
end

local function equip_targets(item, occupied)
    local itemType = tonumber(item and item.ext and item.ext.sdcType) or 0
    local subType = tonumber(item and item.ext and item.ext.sdcSubType) or 0
    local result = {}
    if itemType == 1 and not occupied[1] then result[#result + 1] = 1 end
    if itemType == 8 and not occupied[2] then result[#result + 1] = 2 end
    if itemType == 7 and not occupied[3] then result[#result + 1] = 3 end
    if itemType == 3 and subType >= 1 and subType <= 3 then
        local slotType = 3 + subType
        if not occupied[slotType] then result[#result + 1] = slotType end
    end
    if itemType == 2 then
        local duplicate = false
        for slotType = 7, 9 do
            local equipped = occupied[slotType]
            if equipped and tonumber(equipped.ext and equipped.ext.sdcSubType) == subType then
                duplicate = true
            end
        end
        if not duplicate then
            for slotType = 7, 9 do
                if not occupied[slotType] then result[#result + 1] = slotType end
            end
        end
    end
    return result
end

local function add_kind(actions, used, kind, candidates)
    if not used[kind] and #candidates > 0 then
        actions[#actions + 1] = {kind = kind, candidates = candidates}
    end
end

local function preset_has_content(preset)
    return type(preset) == "table" and type(preset.slots) == "table" and #preset.slots > 0
end

local function build_preset(prepare)
    local slots = {}
    for _, equip in ipairs(prepare.equipSlots or {}) do
        if (tonumber(equip.itemId) or 0) ~= 0 then
            slots[#slots + 1] = {
                containerType = 1, containerDetail = tonumber(equip.slotType) or 0,
                itemId = tonumber(equip.itemId) or 0, ext = equip.ext,
            }
        end
    end
    local packs = pack_map(prepare)
    for _, pack in pairs(packs) do
        for _, item in ipairs(pack.items or {}) do
            if (tonumber(item.itemId) or 0) ~= 0 then
                slots[#slots + 1] = {
                    containerType = 2, containerDetail = tonumber(pack.packType) or 0,
                    itemId = tonumber(item.itemId) or 0, ext = item.ext, expired = false,
                }
            end
        end
    end
    return {
        slotIndex = 1, name = "压测方案", slots = slots,
        amplifierPackSlots = tonumber(packs[1] and packs[1].maxSlots) or 0,
        medicinePackSlots = tonumber(packs[2] and packs[2].maxSlots) or 0,
        keyChainPackSlots = tonumber(packs[4] and packs[4].maxSlots) or 0,
    }
end

local function build_actions(prepare, warehouse, containers, suit, used)
    local actions = {}
    local warehouseItems = warehouse.items or {}
    local packs = pack_map(prepare)
    local occupied = occupied_slots(prepare)
    local suitState = suit.suitState or {}
    local usingSuit = suitState.usingSuit == true
    local freeWarehouse = math.max((tonumber(warehouse.maxSlots) or 0)
        - (tonumber(warehouse.usedSlots) or #warehouseItems), 0)

    if not usingSuit then
        local allItems, equipCandidates = {}, {}
        for _, item in ipairs(warehouseItems) do allItems[#allItems + 1] = item end
        for _, pack in pairs(packs) do
            for _, item in ipairs(pack.items or {}) do allItems[#allItems + 1] = item end
        end
        for _, item in ipairs(allItems) do
            if valid_guid(item.guid) then
                for _, slotType in ipairs(equip_targets(item, occupied)) do
                    equipCandidates[#equipCandidates + 1] = {itemGuid = item.guid, slotType = slotType}
                end
            end
        end
        add_kind(actions, used, "equip", equipCandidates)

        local unequipCandidates = {}
        if freeWarehouse > 0 then
            for slotType, slot in pairs(occupied) do
                if slotType ~= 3 then
                    unequipCandidates[#unequipCandidates + 1] = {slotType = slotType, itemGuid = slot.itemGuid}
                end
            end
        end
        add_kind(actions, used, "unequip", unequipCandidates)

        local moveCandidates = {}
        for _, item in ipairs(warehouseItems) do
            local targetType = pack_type_for_item(item)
            local target = targetType and packs[targetType]
            if valid_guid(item.guid) and target
                and #(target.items or {}) < (tonumber(target.maxSlots) or 0) then
                moveCandidates[#moveCandidates + 1] = {
                    itemGuid = item.guid, fromPackType = 0, toPackType = targetType,
                }
            end
        end
        if freeWarehouse > 0 then
            for packType, pack in pairs(packs) do
                for _, item in ipairs(pack.items or {}) do
                    if valid_guid(item.guid) then
                        moveCandidates[#moveCandidates + 1] = {
                            itemGuid = item.guid, fromPackType = packType, toPackType = 0,
                        }
                    end
                end
            end
        end
        add_kind(actions, used, "move", moveCandidates)

        local swapCandidates = {}
        for packType, pack in pairs(packs) do
            for _, source in ipairs(pack.items or {}) do
                for _, target in ipairs(warehouseItems) do
                    if pack_type_for_item(target) == packType
                        and valid_guid(source.guid) and valid_guid(target.guid) then
                        swapCandidates[#swapCandidates + 1] = {
                            sourceItemGuid = source.guid, sourcePackType = packType,
                            targetItemGuid = target.guid,
                        }
                    end
                end
            end
        end
        add_kind(actions, used, "swap", swapCandidates)

        local useCandidates = {}
        for _, item in ipairs(warehouseItems) do
            local itemType = tonumber(item.ext and item.ext.sdcType) or 0
            if valid_guid(item.guid) and (tonumber(item.ext and item.ext.number) or 0) > 0
                and (itemType == 12 or itemType == 13) then
                useCandidates[#useCandidates + 1] = {itemGuid = item.guid, number = 1}
            end
        end
        add_kind(actions, used, "pack_use", useCandidates)
    end

    local activateCandidates, switchCandidates, moveAllCandidates = {}, {}, {}
    for _, container in ipairs(containers or {}) do
        local packType = tonumber(container.packType) or 0
        local currentId = tonumber(container.currentSdcParameterId) or 0
        for _, level in ipairs(container.levels or {}) do
            local parameterId = tonumber(level.sdcParameterId) or 0
            if tonumber(level.buttonState) == 3 then
                for _, card in ipairs(level.availableCards or {}) do
                    if valid_guid(card.itemGuid) and (tonumber(card.number) or 0) > 0 then
                        activateCandidates[#activateCandidates + 1] = {
                            packType = packType,
                            sdcParameterId = tonumber(card.sdcParameterId) or parameterId,
                            itemGuid = card.itemGuid, number = 1,
                        }
                    end
                end
            end
            if tonumber(level.buttonState) == 1 and level.isCurrent ~= true and level.isExpired ~= true then
                switchCandidates[#switchCandidates + 1] = {
                    packType = packType, sdcParameterId = parameterId,
                }
            end
            if parameterId == currentId and level.isExpired == true then
                local pack = packs[packType]
                if pack and #(pack.items or {}) > 0 then
                    moveAllCandidates[#moveAllCandidates + 1] = {packType = packType}
                end
            end
        end
    end
    add_kind(actions, used, "container_activate", activateCandidates)
    add_kind(actions, used, "container_switch", switchCandidates)
    add_kind(actions, used, "container_move_all", moveAllCandidates)

    if usingSuit and suitState.committed ~= true then
        add_kind(actions, used, "suit_cancel", {{}})
    elseif not usingSuit then
        local suitCandidates = {}
        for _, plan in ipairs(suit.plans or {}) do
            local suitType = tonumber(plan.suitType) or 0
            for _, item in ipairs(warehouseItems) do
                if tonumber(item.ext and item.ext.sdcType) == 9
                    and tonumber(item.ext and item.ext.sdcSubType) == suitType
                    and (tonumber(item.ext and item.ext.number) or 0) > 0 then
                    suitCandidates[#suitCandidates + 1] = {
                        suitType = suitType, planId = tonumber(plan.planId) or 0,
                    }
                    break
                end
            end
        end
        add_kind(actions, used, "suit_use", suitCandidates)
        add_kind(actions, used, "preset_save", {{slotIndex = 1, preset = build_preset(prepare)}})

        local presetCandidates = {}
        for _, preset in ipairs(suit.presets or {}) do
            if (tonumber(preset.slotIndex) or 0) > 0 and preset_has_content(preset) then
                presetCandidates[#presetCandidates + 1] = {slotIndex = tonumber(preset.slotIndex)}
            end
        end
        add_kind(actions, used, "preset_use", presetCandidates)
    end
    return actions
end

function execute(r)
    local checks = {
        {"sdcPrepareSnapshotErrorCode", "拉取战备"},
        {"sdcWarehouseSnapshotErrorCode", "拉取仓库"},
        {"sdcContainerSnapshotErrorCode", "拉取局外容器"},
        {"sdcSuitPresetSnapshotErrorCode", "拉取套装预设"},
    }
    for _, check in ipairs(checks) do
        local err = check_response(check[1], check[2])
        if err then return err end
    end

    local prepare = robot.get("sdcPrepareSnapshot")
    local warehouse = robot.get("sdcWarehouseSnapshot")
    local containers = robot.get("sdcContainerSnapshot")
    local suit = robot.get("sdcSuitPresetSnapshot")
    if type(prepare) ~= "table" or type(warehouse) ~= "table"
        or type(containers) ~= "table" or type(suit) ~= "table" then
        return robot.error(54, "战备管理缺少声明式服务端快照")
    end

    local used = robot.get("sdcPrepareUsedKinds") or {}
    local actions = build_actions(prepare, warehouse, containers, suit, used)
    if #actions == 0 then
        robot.set("sdcPrepareManagementDone", true)
        robot.set("sdcPrepareActionKind", "none")
        return nil
    end

    local action = utils.random_pick(actions)
    local candidate = utils.random_pick(action.candidates) or {}
    robot.set("sdcPrepareActionKind", action.kind)
    robot.set("sdcPrepareActionParams", candidate)
    return nil
end
