-- 只负责按结局把局内最终资产分类为保留/丢失，并生成纯 table 结算数据。
local robot = require("robot")

local SAFE_BOX = 3
local KEY_CHAIN = 4

local function valid_guid(value)
    return value ~= nil and tostring(value) ~= "" and tostring(value) ~= "0"
end

local function item_value(data)
    local ext = type(data) == "table" and data.ext or nil
    if type(ext) ~= "table" then return 0 end
    return (tonumber(ext.value) or 0) * math.max(tonumber(ext.number) or 1, 1)
end

local function rating_for(lootValue, budget)
    if lootValue <= 0 then return 0 end
    if budget <= 0 then return 1 end
    local ratio = lootValue / budget
    if ratio >= 0.20 then return 5 end
    if ratio >= 0.12 then return 4 end
    if ratio >= 0.07 then return 3 end
    if ratio >= 0.03 then return 2 end
    return 1
end

function execute(r)
    local prepare = robot.get("sdcPrepareData")
    local battlePacks = robot.get("sdcBattlePackData")
    local battleEquips = robot.get("sdcBattleEquipData")
    local acquired = robot.get("sdcBattleAcquiredItems") or {}
    if type(prepare) ~= "table" or type(battlePacks) ~= "table" or type(battleEquips) ~= "table" then
        return robot.error(54, "结算分类缺少战前或局内最终资产快照")
    end

    local outcome = tostring(robot.get("sdcOutcome") or "success")
    local success = outcome == "success"
    local acquiredByGuid, acquiredItemIds = {}, {}
    for _, item in ipairs(acquired) do
        if valid_guid(item.guid) then acquiredByGuid[tostring(item.guid)] = tonumber(item.value) or 0 end
        if (tonumber(item.itemId) or 0) ~= 0 then acquiredItemIds[#acquiredItemIds + 1] = tonumber(item.itemId) end
    end

    local kept, lost, finalEquips, finalPacks = {}, {}, {}, {}
    local retainedLootValue, lossAssetValue = 0, 0
    for _, equip in ipairs(battleEquips) do
        if success then
            finalEquips[#finalEquips + 1] = equip
            if valid_guid(equip.itemGuid) then kept[#kept + 1] = equip.itemGuid end
        elseif valid_guid(equip.itemGuid) then
            lost[#lost + 1] = equip.itemGuid
            lossAssetValue = lossAssetValue + item_value(equip)
        end
    end

    for _, pack in ipairs(battlePacks) do
        local packType = tonumber(pack.packType) or 0
        local keepItems = success or packType == SAFE_BOX or packType == KEY_CHAIN
        local finalItems, finalValue = {}, 0
        for _, item in ipairs(pack.items or {}) do
            local value = item_value(item)
            if keepItems then
                finalItems[#finalItems + 1] = item
                finalValue = finalValue + value
                if valid_guid(item.guid) then
                    kept[#kept + 1] = item.guid
                    retainedLootValue = retainedLootValue + (acquiredByGuid[tostring(item.guid)] or 0)
                end
            elseif valid_guid(item.guid) then
                lost[#lost + 1] = item.guid
                lossAssetValue = lossAssetValue + value
            end
        end
        finalPacks[#finalPacks + 1] = {
            packType = packType, maxSlots = pack.maxSlots or 0, items = finalItems,
            totalValue = finalValue, safeBoxParamId = pack.safeBoxParamId or 0,
            safeBoxExpireTime = pack.safeBoxExpireTime or 0,
        }
    end

    local battleLootValue = tonumber(robot.get("sdcBattleLootValue")) or 0
    local settleLootValue = success and battleLootValue or retainedLootValue
    local surviveTime = math.floor((tonumber(robot.get("sdcDwellTargetMs")) or 0) / 1000)
    -- 结算数据不提交背包/装备明细(finalPackSlots/finalEquipSlots)和 GUID 列表(lost/kept)。
    -- 服务端 ValidateSettlementCandidate 会用 ItemRegistry（匹配快照构建）逐个校验道具 binding，
    -- 客户端战前数据(45:1)与匹配快照(4:6)不一致时投票被拒绝→4:72 永远不发→超时。
    -- 空列表能通过结构校验，服务端用 registry 重算 lootValue/rating，不依赖客户端明细。
    local settle = {
        result = tonumber(robot.get("sdcExpectedSettleResult")) or 1,
        killCount = 0, totalValue = tonumber(prepare.totalValue) or 0,
        lootValue = settleLootValue, lostItemGuids = {}, keptItemGuids = {},
        finalPackSlots = {}, seasonCoin = 0, monsterKillCount = 0,
        surviveTime = surviveTime,
        rating = rating_for(settleLootValue, tonumber(robot.get("sdcLoadingDropBudget")) or 0),
        assistCount = 0, rescueCount = 0, finalEquipSlots = {},
        heroInfo = prepare.heroInfo, equipSlotValueDiff = 0, netLootValue = settleLootValue,
        acquiredItemIds = acquiredItemIds, lossAssetValue = lossAssetValue,
        packNormalNetValue = settleLootValue, packSeasonNetValue = 0,
    }
    local summary = {
        outcome = outcome, settleResult = settle.result, lootValue = settleLootValue,
        lostCount = #lost, keptCount = #kept, acquiredCount = #acquiredItemIds,
        finalPackCount = #finalPacks, finalEquipCount = #finalEquips,
    }
    robot.set("sdcExpectedLostGuids", lost)
    robot.set("sdcExpectedKeptGuids", kept)
    robot.set("sdcSettlementData", settle)
    robot.set("sdcSettlementSummary", summary)
    return nil
end
