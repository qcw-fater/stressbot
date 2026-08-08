-- listen_start_loading.lua: 等待当前匹配的战斗开始加载（tcp_listen CMD=4, ACT=6）
-- 多模式通用：排位/漫斗/搜打撤/公会战等所有带类似协议头的玩法共用此脚本。
-- 持久监听队列可能残留旧局推送，必须用 FighterList 中自己的 sessionId 对齐 MatchSucceed。
-- 服务端在 BP/准备阶段（含 SDC 的 commitSDCSuitPresetStartLoading）失败时会发 2:10(返回大厅)，
-- 不能死等 4:6；每个切片结束后检查 2:10 缓存，收到则立即失败走恢复，避免 180s 超时卡住整轮。
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")
local log = require("log")

local START_LOADING_TIMEOUT_MS = 180 * 1000
local POLL_MS = 500
local POLL_SLICE_SEC = 5

local function same_id(left, right)
    if left == nil or right == nil then
        return false
    end
    return tostring(left) == tostring(right)
end

local function valid_id(value)
    return value ~= nil and value ~= 0 and value ~= "0"
end

local function first_path(msg, paths)
    for _, path in ipairs(paths) do
        local value = proto.get_path(msg, path)
        if value ~= nil then
            return value
        end
    end
    return nil
end

local function remaining_seconds(deadlineMs)
    local remainingMs = deadlineMs - utils.time_ms()
    if remainingMs <= 0 then
        return nil
    end
    return math.max(1, math.ceil(remainingMs / 1000))
end

local function parse_candidate(resp, roleId)
    local candidate = {
        battleId = proto.get_path(resp, "battleId"),
        gameType = first_path(resp, {"record.GameType", "record.gameType", "record.gType"}),
        fighterCount = proto.list_size(resp, "record.FighterList"),
        fighters = {},
    }

    local address = proto.get_path(resp, "record.address")
    if address ~= nil then
        local normalized = tostring(address)
        normalized = normalized:gsub("^all://", "")
        normalized = normalized:gsub("^tcp://", "")
        normalized = normalized:gsub("^udp://", "")
        candidate.battleAddress = normalized
    end

    local myPlayerId = tonumber(roleId)
    for i = 1, candidate.fighterCount do
        local fighter = proto.list_get(resp, "record.FighterList", i)
        local playerId = tonumber(proto.get_path(fighter, "playerId")) or 0

        if myPlayerId and playerId == myPlayerId then
            candidate.fighterIndex = i - 1
            candidate.battleSecretKey = proto.get_path(fighter, "secretKey")
            candidate.battleSession = proto.get_path(fighter, "matchData.sessionId")
        end

        local item = {
            playerId = playerId,
            name = tostring(proto.get_path(fighter, "matchData.base.name") or ""),
            teamId = tonumber(proto.get_path(fighter, "matchData.teamId")) or 0,
            fightIndex = tonumber(proto.get_path(fighter, "matchData.index")) or 0,
            serverFightIndex = i - 1,
            camp = tonumber(proto.get_path(fighter, "matchData.camp")) or 0,
            selectHeroes = {},
            selectSkins = {},
        }

        local heroCount = proto.list_size(fighter, "selectHeroes")
        for j = 1, heroCount do
            item.selectHeroes[j] = tonumber(proto.list_get(fighter, "selectHeroes", j)) or 0
        end

        local skinCount = proto.list_size(fighter, "selectSkins")
        for j = 1, skinCount do
            item.selectSkins[j] = tonumber(proto.list_get(fighter, "selectSkins", j)) or 0
        end

        candidate.fighters[i] = item
    end

    -- SDC 专有：掉落预算 + 原始快照字节（其他模式这两个字段不存在，proto.get_path 返回 nil，不影响）
    candidate.sdcDropBudget = tonumber(proto.get_path(resp, "record.sdcDropData.totalBudget")) or 0
    candidate.snapshotWire = proto.serialize(resp)

    return candidate
end

local function candidate_error(candidate, roleId)
    if not valid_id(candidate.battleId) then
        return "battleId 缺失"
    end
    if candidate.battleAddress == nil or candidate.battleAddress == "" then
        return "battleAddress 缺失"
    end
    if candidate.fighterIndex == nil then
        return "fighterIndex 缺失"
    end
    if candidate.battleSecretKey == nil or candidate.battleSecretKey == "" then
        return "battleSecretKey 缺失"
    end
    if not valid_id(candidate.battleSession) then
        return "battleSession 缺失"
    end
    if tonumber(roleId) == nil then
        return "roleId 非法"
    end
    return nil
end

local function store_candidate(candidate)
    robot.set("battleId", candidate.battleId)
    robot.set("battleAddress", candidate.battleAddress)
    robot.set("fighterIndex", candidate.fighterIndex)
    robot.set("battleSecretKey", candidate.battleSecretKey)
    robot.set("battleSession", candidate.battleSession)
    if candidate.gameType ~= nil then
        robot.set("battleGameType", tonumber(candidate.gameType) or candidate.gameType)
    end
    if #candidate.fighters > 0 then
        robot.set("fighterListData", candidate.fighters)
    end
    -- SDC 专有字段（其他模式 sdcDropBudget=0、snapshotWire 为空序列化结果，不设也行）
    robot.set("sdcLoadingDropBudget", candidate.sdcDropBudget or 0)
    if type(candidate.snapshotWire) == "string" and #candidate.snapshotWire > 0 then
        robot.set("sdcLoadingSnapshotWire", candidate.snapshotWire)
    end
end

function execute(r)
    local roleId = robot.get("roleId") or robot.get("playerId")
    local expectedBattleSession = robot.get("battleSession")
    if not valid_id(expectedBattleSession) then
        return robot.error(54, "ListenStartLoading 缺少当前 battleSession: roleId="
            .. tostring(roleId))
    end

    -- 所有旧消息共用一个总等待窗口，不能为每条旧 4:6 重置 180 秒。
    -- 服务端在 BP/准备阶段失败时会发 2:10(返回大厅)，不能死等 4:6；
    -- 每个切片结束后检查 2:10 缓存，收到则立即失败走恢复。
    local deadlineMs = utils.time_ms() + START_LOADING_TIMEOUT_MS
    while true do
        local timeoutSec = remaining_seconds(deadlineMs)
        if timeoutSec == nil then
            return robot.error(31, "等待当前轮 BattleStartLoading 超时: roleId="
                .. tostring(roleId) .. " battleSession=" .. tostring(expectedBattleSession))
        end
        if timeoutSec > POLL_SLICE_SEC then timeoutSec = POLL_SLICE_SEC end

        local err, resp = network.tcp_listen(
            "logic", {cmd=4, act=6}, "Game.BattleStartLoadingS2C", timeoutSec, POLL_MS)
        if err then
            if tonumber(err.code) ~= 31 then
                log.error("ListenStartLoading 等待开始加载失败: service=logic route=4:6 "
                    .. "proto=Game.BattleStartLoadingS2C roleId=" .. tostring(roleId)
                    .. " expectedSession=" .. tostring(expectedBattleSession)
                    .. " remainingSec=" .. tostring(timeoutSec)
                    .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
                return err
            end
            -- 本切片超时：检查服务端是否已发返回大厅(2:10)
            local backErr, backRaw = network.try_tcp_listen("logic", {cmd=2, act=10})
            if backErr == nil and backRaw ~= nil and backRaw ~= "" then
                log.warn("ListenStartLoading 收到返回大厅(2:10)，本轮战斗未启动: roleId="
                    .. tostring(roleId) .. " expectedSession=" .. tostring(expectedBattleSession))
                return robot.error(54, "排位开始加载阶段服务端返回大厅(2:10): roleId="
                    .. tostring(roleId))
            end
            -- 本切片超时且未收到 2:10，继续下一个切片（while 自然回顶）
        else
            local ok, candidate = pcall(parse_candidate, resp, roleId)
            if not ok then
                log.error("ListenStartLoading 解析失败: roleId=" .. tostring(roleId)
                    .. " expectedSession=" .. tostring(expectedBattleSession)
                    .. " err=" .. tostring(candidate))
                return robot.error(12, "ListenStartLoading 解析失败: roleId=" .. tostring(roleId)
                    .. " expectedSession=" .. tostring(expectedBattleSession)
                    .. " err=" .. tostring(candidate))
            end

            if same_id(candidate.battleSession, expectedBattleSession) then
                local invalidReason = candidate_error(candidate, roleId)
                if invalidReason ~= nil then
                    log.error("当前轮开始加载关键字段缺失: roleId=" .. tostring(roleId)
                        .. " reason=" .. invalidReason
                        .. " battleId=" .. tostring(candidate.battleId)
                        .. " fighterIndex=" .. tostring(candidate.fighterIndex)
                        .. " fighterCount=" .. tostring(candidate.fighterCount))
                    return robot.error(54, "当前轮开始加载关键字段缺失: roleId="
                        .. tostring(roleId) .. " reason=" .. invalidReason)
                end

                store_candidate(candidate)
                log.info("开始加载: roleId=" .. tostring(roleId)
                    .. " battleAddress=" .. tostring(candidate.battleAddress)
                    .. " fighterIndex=" .. tostring(candidate.fighterIndex)
                    .. " battleId=" .. tostring(candidate.battleId)
                    .. " battleSession=" .. tostring(candidate.battleSession)
                    .. " fighterCount=" .. tostring(candidate.fighterCount)
                    .. " hasSecretKey=true")
                return nil
            end

            log.warn("丢弃旧 BattleStartLoading: roleId=" .. tostring(roleId)
                .. " expectedSession=" .. tostring(expectedBattleSession)
                .. " candidateSession=" .. tostring(candidate.battleSession)
                .. " candidateBattleId=" .. tostring(candidate.battleId)
                .. " fighterCount=" .. tostring(candidate.fighterCount))
        end
    end
end
