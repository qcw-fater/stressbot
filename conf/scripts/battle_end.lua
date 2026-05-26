-- battle_end.lua: 发送战斗结束（BattleEndC2S CMD=4, ACT=13）
-- 与旧工具一致：构建 PlayerResult（含战斗统计），通过 battle TCP 发送（request-response）
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
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

            -- 阵营结算：CtCamp0(1) → 胜利(rank=1)，其余 → 失败(rank=2)
            local camp = tonumber(f.camp) or 0
            if camp == 1 then  -- CtCamp0 = 1
                proto.set_field(stat, "rank", 1)
                proto.set_field(stat, "result", 1)  -- BRT_WIN = 1
            else
                proto.set_field(stat, "rank", 2)
                proto.set_field(stat, "result", 2)  -- BRT_LOSE = 2
            end

            -- 第一个玩家设为 MVP
            if i == 1 then
                proto.set_field(stat, "mvp", true)
            end

            -- HeroStatistics：击杀1/死亡1/伤害28256
            local heroId = tonumber(f.heroId) or 0
            local skinId = tonumber(f.skinId) or 0
            if heroId > 0 then
                local heroStat = proto.create("Game.HeroBattleStatistics")
                proto.set_field(heroStat, "heroId", heroId)
                proto.set_field(heroStat, "skinId", skinId)
                proto.set_field(heroStat, "killNum", 1)
                proto.set_field(heroStat, "deadNum", 1)
                proto.set_field(heroStat, "damage", 28256)
                proto.set_field(stat, "HeroStatistics", {heroStat})
            end

            playerResults[i] = stat
        end

        if #playerResults > 0 then
            proto.set_field(msg, "playerResult", playerResults)
        end
    end

    -- 使用 request 模式发送（与旧工具 RequestResponse 一致）
    local code, _, sent, recv = network.tcp_request("battle", {cmd=4, act=13}, msg)
    if code ~= 0 then
        log.error("BattleEnd 发送失败: code=" .. tostring(code))
        return code, sent, recv  -- 透传底层 code
    end
    log.info("BattleEnd 已发送")

    -- 关闭连接
    network.close_udp("battle")
    network.close_tcp("battle")

    return 0, sent, recv
end
