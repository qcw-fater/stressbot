-- 只负责在到达开箱时刻时，从 Loading 下发的真实宝箱池选择一个可开启宝箱。
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")
local log = require("log")

local KEY_CHAIN = 4
local NORMAL_CHEST = 0
local KEY_CHEST = 1
local PACK_BY_ITEM_TYPE = {[3] = 1, [4] = 5, [5] = 2, [6] = 4}

local function valid_guid(value)
    return value ~= nil and tostring(value) ~= "" and tostring(value) ~= "0"
end

local function capacities(packs)
    local result = {}
    for _, pack in ipairs(packs or {}) do
        local packType = tonumber(pack.packType) or 0
        result[packType] = (result[packType] or 0)
            + math.max((tonumber(pack.maxSlots) or 0) - #(pack.items or {}), 0)
    end
    return result
end

local function existing_guids(packs, equips)
    local result = {}
    for _, pack in ipairs(packs or {}) do
        for _, item in ipairs(pack.items or {}) do
            if valid_guid(item.guid) then result[tostring(item.guid)] = true end
        end
    end
    for _, equip in ipairs(equips or {}) do
        if valid_guid(equip.itemGuid) then result[tostring(equip.itemGuid)] = true end
    end
    return result
end

local function has_key(packs)
    for _, pack in ipairs(packs or {}) do
        if tonumber(pack.packType) == KEY_CHAIN then
            for _, item in ipairs(pack.items or {}) do
                if valid_guid(item.guid) and tonumber((item.ext or {}).sdcType) == 6 then return true end
            end
        end
    end
    return false
end

local function append_items(chest, free, seen, result)
    if chest == nil then return end
    for i = 1, proto.list_size(chest, "items") do
        local item = proto.list_get(chest, "items", i)
        local guid = proto.get_path(item, "guid")
        local packType = PACK_BY_ITEM_TYPE[tonumber(proto.get_path(item, "ext.sdcType")) or 0]
        if packType and (free[packType] or 0) > 0 and valid_guid(guid)
            and not seen[tostring(guid)] then
            result[#result + 1] = {
                item = proto.get_field_map(item),
                packType = packType,
            }
        end
    end
end

local function extra_key(sourceType, areaIndex, taskGuid, chestIndex)
    if tonumber(sourceType) == 1 then
        return "area:" .. tostring(areaIndex or 0) .. ":" .. tostring(chestIndex or 0)
    end
    return "task:" .. tostring(taskGuid or "") .. ":" .. tostring(chestIndex or 0)
end

local function player_extras(message, roleId)
    local result = {}
    for i = 1, proto.list_size(message, "record.sdcDropData.playerExtraDrops") do
        local extra = proto.list_get(message, "record.sdcDropData.playerExtraDrops", i)
        if tonumber(proto.get_path(extra, "playerId")) == tonumber(roleId) then
            for j = 1, proto.list_size(extra, "chests") do
                local chest = proto.list_get(extra, "chests", j)
                result[extra_key(
                    proto.get_path(chest, "sourceType"), proto.get_path(chest, "areaIndex"),
                    proto.get_path(chest, "taskGuid"), proto.get_path(chest, "chestIndex")
                )] = chest
            end
        end
    end
    return result
end

local function add_group(group, kind, sourceId, extras, result, free, seen, opened, keyAvailable)
    for i = 1, proto.list_size(group, "chests") do
        local chest = proto.list_get(group, "chests", i)
        local sourceKey = kind .. ":" .. tostring(sourceId or "") .. ":" .. tostring(i - 1)
        local chestType = tonumber(proto.get_path(chest, "chestType")) or NORMAL_CHEST
        if (chestType == NORMAL_CHEST or chestType == KEY_CHEST)
            and (chestType ~= KEY_CHEST or keyAvailable) and not opened[sourceKey] then
            local eligible = {}
            append_items(chest, free, seen, eligible)
            append_items(extras[sourceKey], free, seen, eligible)
            result[#result + 1] = {
                sourceKey = sourceKey,
                chestId = tonumber(proto.get_path(chest, "chestId")) or 0,
                chestType = chestType,
                eligible = eligible,
                hasPlayerExtra = extras[sourceKey] ~= nil,
            }
        end
    end
end

local function collect_chests(message, roleId, free, seen, opened, packs)
    local result = {}
    local extras = player_extras(message, roleId)
    local keyAvailable = has_key(packs)
    for i = 1, proto.list_size(message, "record.sdcDropData.areaGroups") do
        local group = proto.list_get(message, "record.sdcDropData.areaGroups", i)
        add_group(group, "area", proto.get_path(group, "areaIndex"), extras,
            result, free, seen, opened, keyAvailable)
    end
    for i = 1, proto.list_size(message, "record.sdcDropData.taskGroups") do
        local group = proto.list_get(message, "record.sdcDropData.taskGroups", i)
        add_group(group, "task", proto.get_path(group, "taskGuid"), extras,
            result, free, seen, opened, keyAvailable)
    end
    for i = 1, proto.list_size(message, "record.sdcDropData.monsterDropGroups") do
        local group = proto.list_get(message, "record.sdcDropData.monsterDropGroups", i)
        add_group(group, "monster", proto.get_path(group, "unitId"), {},
            result, free, seen, opened, keyAvailable)
    end
    return result
end

function execute(r)
    robot.set("sdcChestSelected", false)
    robot.set("sdcSelectedChest", {})
    if robot.get("sdcLootEventDone") then return nil end

    local startMs = tonumber(robot.get("sdcDwellStartMs"))
    local nextAt = tonumber(robot.get("sdcNextChestOpenAtMs"))
    local openTarget = tonumber(robot.get("sdcChestOpenTarget")) or 1
    local openCount = tonumber(robot.get("sdcChestOpenCount")) or 0
    if not startMs or not nextAt then return robot.error(54, "开箱选择缺少计时状态") end
    if openCount >= openTarget then
        robot.set("sdcLootEventDone", true)
        return nil
    end
    local elapsed = utils.time_ms() - startMs
    if elapsed < nextAt then return nil end

    local wire = robot.get("sdcLoadingSnapshotWire")
    if type(wire) ~= "string" or #wire == 0 then return robot.error(54, "开箱选择缺少 Loading 快照") end
    local ok, loading = pcall(proto.parse, "Game.BattleStartLoadingS2C", wire)
    if not ok then return robot.error(12, "解析宝箱池失败: " .. tostring(loading)) end

    local packs = robot.get("sdcBattlePackData") or {}
    local chests = collect_chests(
        loading, robot.get("roleId") or robot.get("playerId"), capacities(packs),
        existing_guids(packs, robot.get("sdcBattleEquipData") or {}),
        robot.get("sdcOpenedChestKeys") or {}, packs
    )
    if #chests == 0 then
        robot.set("sdcLootEventDone", true)
        log.info("搜打撤真实宝箱池已无可开启宝箱")
        return nil
    end
    robot.set("sdcSelectedChest", chests[utils.random_int(#chests) + 1])
    robot.set("sdcChestElapsedMs", elapsed)
    robot.set("sdcChestSelected", true)
    return nil
end
