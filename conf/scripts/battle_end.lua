-- battle_end.lua: 发送战斗结束（BattleEndC2S CMD=4, ACT=13）
-- 使用 tcp_request：服务端 handleResult 同步结算后发 ACK，客户端等待确认再关闭。
-- 连接层已修复 ACK/FIN 竞态（ctx.Done 时先 drain response channel）。
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    local battleId = robot.get("battleId")
    local fighterIndex = robot.get("fighterIndex")
    local battleSession = robot.get("battleSession")

    -- 旧 Robot 工具在发送 BattleEnd(4:13) 前先关闭 UDP。
    network.close_udp("battle")

    local msg = proto.create("Game.BattleEndC2S")
    proto.set_field(msg, "resultMD5", "1234567890")

    -- 构建 PlayerResult（与旧工具 buildBattleStatistics 一致）
    local fighters = robot.get("fighterListData")
    local fighterCount = 0
    if fighters and type(fighters) == "table" then
        fighterCount = #fighters
        local playerResults = {}
        for i, f in ipairs(fighters) do
            local stat = proto.create("Game.PlayerBattleStatistics")
            proto.set_field(stat, "name", tostring(f.name or ""))
            proto.set_field(stat, "playerId", f.playerId or 0)
            proto.set_field(stat, "fightIndex", f.fightIndex or 0)
            proto.set_field(stat, "teamId", f.teamId or 0)
            proto.set_field(stat, "camp", f.camp or 0)

            -- 旧工具：rank 按 CampType 顺序 1..10；CtCamp0 胜利，其余失败。
            local camp = tonumber(f.camp) or 0
            if camp == 1 then  -- CtCamp0 = 1
                proto.set_field(stat, "rank", 1)
                proto.set_field(stat, "result", 1)  -- BRT_WIN = 1
            else
                local rank = camp
                if rank < 1 then
                    rank = 2
                end
                proto.set_field(stat, "rank", rank)
                proto.set_field(stat, "result", 2)  -- BRT_LOSE = 2
            end

            -- 第一个玩家设为 MVP
            if i == 1 then
                proto.set_field(stat, "mvp", true)
            end

            -- 旧工具：武道会(GT_BUDOKAI=4)遍历所有 selectHeroes。
            -- 其它玩法只上报第一个英雄。
            local heroStats = {}
            local heroes = f.selectHeroes or {}
            local skins = f.selectSkins or {}
            local gameType = tonumber(robot.get("battleGameType")) or 0
            local heroCount = 0
            if gameType == 4 then
                heroCount = #heroes
            elseif heroes[1] then
                heroCount = 1
            end
            for j = 1, heroCount do
                local heroId = tonumber(heroes[j]) or 0
                if heroId > 0 then
                    local heroStat = proto.create("Game.HeroBattleStatistics")
                    proto.set_field(heroStat, "heroId", heroId)
                    proto.set_field(heroStat, "skinId", tonumber(skins[j]) or 0)
                    proto.set_field(heroStat, "killNum", 1)
                    proto.set_field(heroStat, "deadNum", 1)
                    if gameType == 4 then
                        proto.set_field(heroStat, "damage", 28257)
                    else
                        proto.set_field(heroStat, "damage", 28256)
                    end
                    heroStats[#heroStats + 1] = heroStat
                end
            end
            if #heroStats > 0 then
                proto.set_field(stat, "HeroStatistics", heroStats)
            end

            playerResults[i] = stat
        end

        if #playerResults > 0 then
            proto.set_field(msg, "playerResult", playerResults)
        end
    else
        log.warn("BattleEnd 缺少 fighterListData，将不填充 playerResult: battleId="
            .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " battleSession=" .. tostring(battleSession))
    end

    -- 使用 tcp_request 等待服务端 ACK（handleResult 同步结算约 3.6s）
    local code, _, sent, recv = network.tcp_request("battle", {cmd=4, act=13}, msg)
    if code ~= 0 then
        log.error("BattleEnd 请求失败: service=battle route=4:13 battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " battleSession=" .. tostring(battleSession)
            .. " fighterCount=" .. tostring(fighterCount)
            .. " code=" .. tostring(code)
            .. " sent=" .. tostring(sent)
            .. " recv=" .. tostring(recv))
        return code, sent, recv
    end
    log.info("BattleEnd 已确认: battleId=" .. tostring(battleId)
        .. " fighterIndex=" .. tostring(fighterIndex)
        .. " fighterCount=" .. tostring(fighterCount)
        .. " sent=" .. tostring(sent)
        .. " recv=" .. tostring(recv))

    -- 收到 ACK 后关闭 Battle TCP，清理战斗状态
    network.close_tcp("battle")
    robot.delete("fighterListData")
    robot.delete("battleSecretKey")
    robot.delete("battleAddress")
    robot.delete("battleSession")
    robot.delete("battleId")
    robot.delete("battleArea")

    return 0, sent, recv
end
