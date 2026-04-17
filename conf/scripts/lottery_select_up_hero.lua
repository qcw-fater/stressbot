-- lottery_select_up_hero.lua: 选择抽卡 UP 英雄
local network = require("network")
local robot = require("robot")
local proto = require("proto")

function execute(r)
    -- 各 pool 的 UP 英雄列表
    local upHeroes = {
        [1] = {123, 113, 153, 109, 158, 130, 142, 140, 138, 150, 148, 160, 143, 118, 124, 156},
        [2] = {159, 134, 125, 127, 122, 110}
    }

    local poolId = math.random(1, 2)
    local heroes = upHeroes[poolId]
    if not heroes or #heroes == 0 then
        return 0
    end

    local heroId = heroes[math.random(#heroes)]

    local msg = proto.create("Game.LotterySelectUpHeroC2S")
    proto.set_field(msg, "lotteryPoolId", poolId)
    proto.set_field(msg, "heroId", heroId)

    network.send("logic", 23, 12, msg)
    return 0
end
