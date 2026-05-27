-- listen_start_loading.lua: 等待战斗开始加载（tcp_listen CMD=4, ACT=6）
-- 从 BattleStartLoadingS2C 提取战斗服地址、fighterIndex、battleId、secretKey
-- 与旧 Robot 工具 SetLoadingInfo 逻辑一致：
--   battleId = 顶层 BattleId
--   fighterIndex = FighterList 中自己的索引（0-based）
--   secretKey = FighterList 中自己的 SecretKey
--   battleSession = FighterList 中自己的 matchData.sessionId
-- 同时存储完整的 fighterList 供 BattleEnd 构建结算数据使用
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    -- 轮询监听开始加载消息，超时 180 秒（3 分钟），轮询 500 毫秒
    local resp, recv = network.tcp_listen("logic", {cmd=4, act=6}, "Game.BattleStartLoadingS2C", 180, 500)
    if not resp then
        log.error("ListenStartLoading 超时")
        return 31, 0, recv  -- 31=LISTEN_TIMEOUT
    end

    local fighterList = nil

    local ok, err = pcall(function()
        local fieldMap = proto.get_field_map(resp)

        -- 提取 battleId（顶层字段，int64 大整数以字符串形式保留精度）
        if fieldMap.battleId then
            robot.set("battleId", fieldMap.battleId)
        end

        -- 提取 record 中的战斗信息
        local record = fieldMap.record
        if record then
            -- 战斗服地址（去除协议前缀）
            if record.address then
                local addr = tostring(record.address)
                addr = addr:gsub("^all://", "")
                addr = addr:gsub("^tcp://", "")
                addr = addr:gsub("^udp://", "")
                robot.set("battleAddress", addr)
            end

            -- 从 FighterList 中找到自己的信息
            -- 旧工具：遍历 FighterList，匹配 playerId，提取 fighterIndex/secretKey/sessionId
            local myPlayerId = robot.get("roleId") or robot.get("playerId")
            fighterList = record.FighterList
            local gameType = record.GameType or record.gameType or record.gType
            if gameType then
                robot.set("battleGameType", tonumber(gameType) or gameType)
            end
            if fighterList and myPlayerId then
                for i, fighter in ipairs(fighterList) do
                    local fid = tonumber(fighter.playerId)
                    if fid and fid == tonumber(myPlayerId) then
                        robot.set("fighterIndex", i - 1)  -- 0-based index
                        -- 提取自己的 UDP 秘钥
                        if fighter.secretKey then
                            robot.set("battleSecretKey", fighter.secretKey)
                        end
                        -- 提取 matchData.sessionId（校验ID）
                        if fighter.matchData and fighter.matchData.sessionId then
                            robot.set("battleSession", fighter.matchData.sessionId)
                        end
                        break
                    end
                end
            end
        end
    end)

    -- 存储完整的 fighterList 供 BattleEnd 构建结算数据
    if fighterList then
        local fighters = {}
        for i, fighter in ipairs(fighterList) do
            local f = {}
            f.playerId = tonumber(fighter.playerId) or 0
            local md = fighter.matchData
            if md then
                f.name = md.base and md.base.name or ""
                f.teamId = md.teamId or 0
                f.fightIndex = md.index or 0
                f.camp = md.camp or 0
            else
                f.name = ""
                f.teamId = 0
                f.fightIndex = 0
                f.camp = 0
            end
            f.selectHeroes = {}
            local heroes = fighter.selectHeroes or {}
            for j, heroId in ipairs(heroes) do
                f.selectHeroes[j] = tonumber(heroId) or 0
            end
            f.selectSkins = {}
            local skins = fighter.selectSkins or {}
            for j, skinId in ipairs(skins) do
                f.selectSkins[j] = tonumber(skinId) or 0
            end
            fighters[i] = f
        end
        robot.set("fighterListData", fighters)
    end

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
