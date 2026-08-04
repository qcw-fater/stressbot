-- 搜打撤专用开始加载：提取通用入局信息，并保存服务器下发的本局原始快照。
-- 此阶段只保存“可用资源”，不选择道具、不修改背包、不计算收益。
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

local function first_path(msg, paths)
    for _, path in ipairs(paths) do
        local value = proto.get_path(msg, path)
        if value ~= nil then
            return value
        end
    end
    return nil
end

function execute(r)
    local roleId = robot.get("roleId") or robot.get("playerId")
    local err, resp = network.tcp_listen("logic", {cmd=4, act=6}, "Game.BattleStartLoadingS2C", 180, 500)
    if err then
        log.error("等待搜打撤开始加载失败: roleId=" .. tostring(roleId)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    local fighterCount = 0
    local loadingDropBudget = 0
    local ok, parseErr = pcall(function()
        local battleId = proto.get_path(resp, "battleId")
        if battleId then
            robot.set("battleId", battleId)
        end

        local address = proto.get_path(resp, "record.address")
        if address then
            local addr = tostring(address)
            addr = addr:gsub("^all://", "")
            addr = addr:gsub("^tcp://", "")
            addr = addr:gsub("^udp://", "")
            robot.set("battleAddress", addr)
        end

        local gameType = first_path(resp, {"record.GameType", "record.gameType", "record.gType"})
        if gameType then
            robot.set("battleGameType", tonumber(gameType) or gameType)
        end

        local myPlayerId = tonumber(roleId)
        fighterCount = proto.list_size(resp, "record.FighterList")
        local fighters = {}
        for i = 1, fighterCount do
            local fighter = proto.list_get(resp, "record.FighterList", i)
            local playerId = tonumber(proto.get_path(fighter, "playerId")) or 0
            if myPlayerId and playerId == myPlayerId then
                robot.set("fighterIndex", i - 1)
                local secretKey = proto.get_path(fighter, "secretKey")
                if secretKey then
                    robot.set("battleSecretKey", secretKey)
                end
                local sessionId = proto.get_path(fighter, "matchData.sessionId")
                if sessionId then
                    robot.set("battleSession", sessionId)
                end
            end

            local fighterData = {
                playerId = playerId,
                name = tostring(proto.get_path(fighter, "matchData.base.name") or ""),
                teamId = proto.get_path(fighter, "matchData.teamId") or 0,
                fightIndex = tonumber(proto.get_path(fighter, "matchData.index")) or 0,
                serverFightIndex = i - 1,
                camp = tonumber(proto.get_path(fighter, "matchData.camp")) or 0,
                selectHeroes = {},
                selectSkins = {},
            }
            local heroCount = proto.list_size(fighter, "selectHeroes")
            for j = 1, heroCount do
                fighterData.selectHeroes[j] = tonumber(proto.list_get(fighter, "selectHeroes", j)) or 0
            end
            local skinCount = proto.list_size(fighter, "selectSkins")
            for j = 1, skinCount do
                fighterData.selectSkins[j] = tonumber(proto.list_get(fighter, "selectSkins", j)) or 0
            end
            fighters[i] = fighterData
        end
        if #fighters > 0 then
            robot.set("fighterListData", fighters)
        end

        loadingDropBudget = tonumber(proto.get_path(resp, "record.sdcDropData.totalBudget")) or 0
        robot.set("sdcLoadingDropBudget", loadingDropBudget)
        robot.set("sdcLoadingSnapshotWire", proto.serialize(resp))
    end)

    if not ok then
        log.error("搜打撤开始加载解析失败: roleId=" .. tostring(roleId)
            .. " fighterCount=" .. tostring(fighterCount) .. " err=" .. tostring(parseErr))
        return robot.error(12, "搜打撤开始加载解析失败: " .. tostring(parseErr))
    end

    local battleAddress = robot.get("battleAddress")
    local battleId = robot.get("battleId")
    local fighterIndex = robot.get("fighterIndex")
    local battleSecretKey = robot.get("battleSecretKey")
    local battleSession = robot.get("battleSession")
    local snapshotWire = robot.get("sdcLoadingSnapshotWire")
    if not battleAddress or not battleId or fighterIndex == nil or not battleSecretKey
        or not battleSession or type(snapshotWire) ~= "string" or #snapshotWire == 0 then
        return robot.error(54, "搜打撤开始加载后关键字段或原始快照缺失: roleId=" .. tostring(roleId)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " snapshotBytes=" .. tostring(type(snapshotWire) == "string" and #snapshotWire or 0))
    end

    log.info("搜打撤开始加载完成: roleId=" .. tostring(roleId)
        .. " battleId=" .. tostring(battleId)
        .. " fighterIndex=" .. tostring(fighterIndex)
        .. " fighterCount=" .. tostring(fighterCount)
        .. " dropBudget=" .. tostring(loadingDropBudget)
        .. " snapshotBytes=" .. tostring(#snapshotWire)
        .. " selectedLoot=0")
    return nil
end
