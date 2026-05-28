-- battle_end.lua: 发送战斗结束（BattleEndC2S CMD=4, ACT=13）
-- 使用 tcp_send 而非 tcp_request：服务端 handleResult 同步执行结算（含 COS 上传），
-- ACK 与 TCP FIN 间隔不足 1 秒，tcp_request 容易因 CONN_DROPPED 失败。
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    -- 旧 Robot 工具在发送 BattleEnd(4:13) 前先关闭 UDP。
    network.close_udp("battle")

    local msg = proto.create("Game.BattleEndC2S")
    proto.set_field(msg, "resultMD5", "1234567890")

    -- 构建 PlayerResult（与旧工具 buildBattleStatistics 一致）
    local fighters = robot.get("fighterListData")
    if fighters and type(fighters) == "table" then
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

            -- 旧工具：武道会(GT_BUDOKAI=4)遍历所有 selectHeroes；
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
    end

    -- 使用 tcp_send 只发不等响应，避免服务端结算阻塞导致 CONN_DROPPED
    local code, sent = network.tcp_send("battle", {cmd=4, act=13}, msg)
    if code ~= 0 then
        log.error("BattleEnd 发送失败: code=" .. tostring(code))
        return code, sent, 0
    end
    log.info("BattleEnd 已发送")

    -- 旧工具：收到 BattleEnd 响应后关闭 Battle TCP 并清理战斗状态。
    network.close_tcp("battle")
    robot.delete("fighterListData")
    robot.delete("battleSecretKey")
    robot.delete("battleAddress")
    robot.delete("battleSession")
    robot.delete("battleId")
    robot.delete("battleArea")

    return 0, sent, 0
end
