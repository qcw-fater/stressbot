-- 声明式绑定暂不支持递归 message/repeated message；本脚本只构造并提交一次 4:69 结算请求。
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

local function build_ext(data)
    if type(data) ~= "table" then return nil end
    local ext = proto.create("Game.SDCItemExtInfo")
    for _, field in ipairs({"sdcType", "sdcSubType", "durability", "maxDurability", "capacity",
        "value", "number", "bindHeroId", "originalValue"}) do
        proto.set_field(ext, field, data[field] or 0)
    end
    if type(data.affixIds) == "table" and #data.affixIds > 0 then proto.set_field(ext, "affixIds", data.affixIds) end
    return ext
end

local function build_item(data, packType)
    local item = proto.create("Game.SDCWarehouseItem")
    proto.set_field(item, "guid", data.guid or 0)
    proto.set_field(item, "itemId", data.itemId or 0)
    local ext = build_ext(data.ext)
    if ext then proto.set_field(item, "ext", ext) end
    proto.set_field(item, "containerType", 2)
    proto.set_field(item, "containerDetail", packType)
    return item
end

local function build_equip(data)
    local slot = proto.create("Game.SDCEquipSlot")
    proto.set_field(slot, "slotType", data.slotType or 0)
    proto.set_field(slot, "itemGuid", data.itemGuid or 0)
    proto.set_field(slot, "itemId", data.itemId or 0)
    local ext = build_ext(data.ext)
    if ext then proto.set_field(slot, "ext", ext) end
    return slot
end

local function build_hero(data)
    if type(data) ~= "table" then return nil end
    local hero = proto.create("Game.SDCHeroTalentAwakeInfo")
    proto.set_field(hero, "heroId", data.heroId or 0)
    if type(data.extraAwakeIds) == "table" and #data.extraAwakeIds > 0 then
        proto.set_field(hero, "extraAwakeIds", data.extraAwakeIds)
    end
    if type(data.extraEquipTalents) == "table" and #data.extraEquipTalents > 0 then
        proto.set_field(hero, "extraEquipTalents", data.extraEquipTalents)
    end
    return hero
end

local function build_settle(data)
    local settle = proto.create("Game.SDCBattleSettleData")
    for _, field in ipairs({"result", "killCount", "totalValue", "lootValue", "seasonCoin",
        "monsterKillCount", "surviveTime", "rating", "assistCount", "rescueCount",
        "equipSlotValueDiff", "netLootValue", "lossAssetValue", "packNormalNetValue",
        "packSeasonNetValue"}) do
        proto.set_field(settle, field, data[field] or 0)
    end
    for _, field in ipairs({"lostItemGuids", "keptItemGuids", "acquiredItemIds"}) do
        if type(data[field]) == "table" and #data[field] > 0 then proto.set_field(settle, field, data[field]) end
    end
    local packs = {}
    for _, source in ipairs(data.finalPackSlots or {}) do
        local pack = proto.create("Game.SDCPackSlot")
        proto.set_field(pack, "packType", source.packType or 0)
        proto.set_field(pack, "maxSlots", source.maxSlots or 0)
        local items = {}
        for _, item in ipairs(source.items or {}) do items[#items + 1] = build_item(item, source.packType or 0) end
        if #items > 0 then proto.set_field(pack, "items", items) end
        proto.set_field(pack, "totalValue", source.totalValue or 0)
        proto.set_field(pack, "safeBoxParamId", source.safeBoxParamId or 0)
        proto.set_field(pack, "safeBoxExpireTime", source.safeBoxExpireTime or 0)
        packs[#packs + 1] = pack
    end
    if #packs > 0 then proto.set_field(settle, "finalPackSlots", packs) end
    local equips = {}
    for _, source in ipairs(data.finalEquipSlots or {}) do equips[#equips + 1] = build_equip(source) end
    if #equips > 0 then proto.set_field(settle, "finalEquipSlots", equips) end
    local hero = build_hero(data.heroInfo)
    if hero then proto.set_field(settle, "heroInfo", hero) end
    return settle
end

function execute(r)
    local battleId = robot.get("battleId")
    local playerId = tonumber(robot.get("roleId") or robot.get("playerId")) or 0
    local fighters = robot.get("fighterListData")
    local settleData = robot.get("sdcSettlementData")
    if battleId == nil or tostring(battleId) == "0" or playerId == 0 then
        return robot.error(54, "SDC 中途结算缺少 battleId 或 playerId")
    end
    if type(fighters) ~= "table" or #fighters == 0 or type(settleData) ~= "table" then
        return robot.error(54, "SDC 中途结算缺少花名册或分类结果")
    end

    local selfSettle = build_settle(settleData)
    local stats, selfIndex = {}, nil
    for i, fighter in ipairs(fighters) do
        local clientIndex = tonumber(fighter.fightIndex)
        local serverIndex = tonumber(fighter.serverFightIndex)
        if clientIndex == nil or serverIndex == nil then
            return robot.error(54, "结算花名册缺少客户端或服务器战斗索引")
        end
        local stat = proto.create("Game.PlayerBattleStatistics")
        proto.set_field(stat, "name", tostring(fighter.name or ""))
        proto.set_field(stat, "playerId", fighter.playerId or 0)
        proto.set_field(stat, "fightIndex", clientIndex)
        proto.set_field(stat, "serverFightIndex", serverIndex)
        proto.set_field(stat, "teamId", fighter.teamId or 0)
        proto.set_field(stat, "camp", fighter.camp or 0)
        if tonumber(fighter.playerId) == playerId then
            proto.set_field(stat, "result", tonumber(robot.get("sdcExpectedBattleResult")) or 1)
            proto.set_field(stat, "sdcRetractTime", settleData.surviveTime or 0)
            proto.set_field(stat, "sdcSettleData", selfSettle)
            selfIndex = serverIndex
        end
        stats[i] = stat
    end
    if selfIndex == nil then return robot.error(54, "结算花名册中未找到当前玩家") end

    local battleEnd = proto.create("Game.BattleEndC2S")
    proto.set_field(battleEnd, "resultMD5", "1234567890")
    proto.set_field(battleEnd, "playerResult", stats)
    local msg = proto.create("Game.BattleMidwaySettlementC2S")
    proto.set_field(msg, "brData", battleEnd)
    proto.set_field(msg, "settlementPlayerId", playerId)
    proto.set_field(msg, "frameIndex", tonumber(robot.get("frameCount")) or 0)
    proto.set_field(msg, "settlementFighterIndex", selfIndex)

    local err, resp = network.tcp_request("battle", {cmd = 4, act = 69}, msg,
        "Game.BattleMidwaySettlementS2C", 180)
    if err then return err end
    local summary = robot.get("sdcSettlementSummary") or {}
    robot.set("sdcSettleResult", summary)
    log.info("搜打撤中途结算已确认: battleId=" .. tostring(battleId)
        .. " outcome=" .. tostring(summary.outcome)
        .. " echoedServerIdx=" .. tostring(proto.get_path(resp, "settlementFighterIndex")))
    return nil
end
