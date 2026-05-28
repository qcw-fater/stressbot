-- listen_start_loading.lua: 等待战斗开始加载（tcp_listen CMD=4, ACT=6）
-- 从 BattleStartLoadingS2C 提取战斗服地址、fighterIndex、battleId、secretKey
-- 与旧 Robot 工具 SetLoadingInfo 逻辑一致：
--   battleId = 顶层 BattleId
--   fighterIndex = FighterList 中自己的索引（0-based）
--   secretKey = FighterList 中自己的 SecretKey
--   battleSession = FighterList 中自己的 matchData.sessionId
-- 同时存储结算需要的 fighterList 裁剪字段
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
    -- 轮询监听开始加载消息，超时 180 秒（3 分钟），轮询 500 毫秒
    local resp, recv = network.tcp_listen("logic", {cmd=4, act=6}, "Game.BattleStartLoadingS2C", 180, 500)
    if not resp then
        log.error("ListenStartLoading 超时")
        return 31, 0, recv  -- 31=LISTEN_TIMEOUT
    end

    local ok, err = pcall(function()
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

        local myPlayerId = tonumber(robot.get("roleId") or robot.get("playerId"))
        local fighterCount = proto.list_size(resp, "record.FighterList")
        local fighters = {}

        for i = 1, fighterCount do
            local fighter = proto.list_get(resp, "record.FighterList", i)
            local playerId = tonumber(proto.get_path(fighter, "playerId")) or 0

            if myPlayerId and playerId == myPlayerId then
                robot.set("fighterIndex", i - 1)  -- 0-based index

                local secretKey = proto.get_path(fighter, "secretKey")
                if secretKey then
                    robot.set("battleSecretKey", secretKey)
                end

                local sessionId = proto.get_path(fighter, "matchData.sessionId")
                if sessionId then
                    robot.set("battleSession", sessionId)
                end
            end

            local f = {}
            f.playerId = playerId
            f.name = tostring(proto.get_path(fighter, "matchData.base.name") or "")
            f.teamId = tonumber(proto.get_path(fighter, "matchData.teamId")) or 0
            f.fightIndex = tonumber(proto.get_path(fighter, "matchData.index")) or 0
            f.camp = tonumber(proto.get_path(fighter, "matchData.camp")) or 0
            f.selectHeroes = {}
            f.selectSkins = {}

            local heroCount = proto.list_size(fighter, "selectHeroes")
            for j = 1, heroCount do
                f.selectHeroes[j] = tonumber(proto.list_get(fighter, "selectHeroes", j)) or 0
            end

            local skinCount = proto.list_size(fighter, "selectSkins")
            for j = 1, skinCount do
                f.selectSkins[j] = tonumber(proto.list_get(fighter, "selectSkins", j)) or 0
            end

            fighters[i] = f
        end

        if #fighters > 0 then
            robot.set("fighterListData", fighters)
        end
    end)

    if ok then
        log.info("开始加载: battleAddress=" .. tostring(robot.get("battleAddress"))
            .. " fighterIndex=" .. tostring(robot.get("fighterIndex"))
            .. " battleId=" .. tostring(robot.get("battleId"))
            .. " battleSession=" .. tostring(robot.get("battleSession")))
    else
        log.error("ListenStartLoading 解析失败: " .. tostring(err))
        return 54, 0, recv  -- 54=LUA_EXIT_CODE
    end

    return 0, 0, recv
end
