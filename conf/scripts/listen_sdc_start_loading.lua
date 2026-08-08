-- 搜打撤专用开始加载：提取通用入局信息，并保存服务器下发的本局原始快照。
-- 此阶段只保存“可用资源”，不选择道具、不修改背包、不计算收益。
-- 注意：服务端 logic 在 commitSDCSuitPresetStartLoading 失败（套装提交/冻结快照/落盘/券校验）时
-- 会吞掉 4:6 并改发 2:10(MainBackLobby) 把玩家拉回大厅。这里不能死等 4:6，需在轮询间隙
-- 检查 2:10 缓存队列，一旦收到返回大厅就立即失败走恢复，避免 180s 超时卡住整轮。
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")
local log = require("log")

local TOTAL_WAIT_MS = 180 * 1000
local POLL_SLICE_SEC = 5
local POLL_SLICE_MS = POLL_SLICE_SEC * 1000

local function first_path(msg, paths)
    for _, path in ipairs(paths) do
        local value = proto.get_path(msg, path)
        if value ~= nil then
            return value
        end
    end
    return nil
end

-- 非阻塞检查服务端是否已下发返回大厅(2:10)。返回 true 表示已收到，本轮加载被服务端中止。
local function serverReturnedToLobby()
    local err, raw = network.try_tcp_listen("logic", {cmd=2, act=10})
    if err ~= nil and tonumber(err.code) ~= 31 then
        log.warn("检查返回大厅推送失败: code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
    end
    return raw ~= nil and raw ~= ""
end

-- 轮询等待开始加载(4:6)，每个切片结束后检查 2:10 返回大厅；收到则立即以业务错误返回。
local function waitForStartLoading(roleId)
    local deadlineMs = utils.time_ms() + TOTAL_WAIT_MS
    while true do
        local remainingMs = deadlineMs - utils.time_ms()
        if remainingMs <= 0 then
            return robot.error(31, "等待搜打撤开始加载超时: roleId=" .. tostring(roleId)
                .. " timeout=" .. tostring(TOTAL_WAIT_MS) .. "ms")
        end
        local sliceSec = math.ceil(remainingMs / 1000)
        if sliceSec > POLL_SLICE_SEC then sliceSec = POLL_SLICE_SEC end

        local err, resp = network.tcp_listen("logic", {cmd=4, act=6},
            "Game.BattleStartLoadingS2C", sliceSec, 500)
        if err == nil then
            return nil, resp
        end
        -- code 31 = 本切片超时，继续下一轮检查；其它错误直接透传
        if tonumber(err.code) ~= 31 then
            log.error("等待搜打撤开始加载失败: roleId=" .. tostring(roleId)
                .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
            return err
        end

        if serverReturnedToLobby() then
            return robot.error(54, "服务端在开始加载阶段把玩家拉回大厅(2:10)，本轮战斗未启动: roleId="
                .. tostring(roleId) .. "（常见于套装提交/冻结快照/落盘失败）")
        end
    end
end

function execute(r)
    local roleId = robot.get("roleId") or robot.get("playerId")
    local err, resp = waitForStartLoading(roleId)
    if err then
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
