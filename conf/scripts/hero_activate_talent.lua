-- hero_activate_talent.lua: 激活英雄天赋（需要找到下一个可激活的天赋位）
local network = require("network")
local robot = require("robot")
local proto = require("proto")

function execute(r)
    local heroIds = robot.get("heroIdList")
    if not heroIds or type(heroIds) ~= "table" or #heroIds == 0 then
        return 0, 0, 0
    end

    local heroId = heroIds[math.random(#heroIds)]

    -- 从 playerData 中查找天赋状态
    local talentIndex = -1
    local playerData = robot.get("playerData")
    if playerData and playerData.loginHeroData and playerData.loginHeroData.heroData
        and playerData.loginHeroData.heroData.heroList then
        for _, hero in ipairs(playerData.loginHeroData.heroData.heroList) do
            if hero.heroId == heroId and hero.equipTalent then
                -- 找到第一个值为 0 的位置
                for i, val in ipairs(hero.equipTalent) do
                    if val == 0 then
                        talentIndex = i - 1  -- 0-based
                        break
                    end
                end
                break
            end
        end
    end

    -- 没有可激活的天赋位，跳过
    if talentIndex < 0 then
        return 0, 0, 0
    end

    local msg = proto.create("Game.HeroActivateTalentC2S")
    proto.set_field(msg, "heroId", heroId)
    proto.set_field(msg, "index", talentIndex)

    local _, sent = network.tcp_send("logic", {cmd=6, act=5}, msg)
    return 0, sent, 0
end
