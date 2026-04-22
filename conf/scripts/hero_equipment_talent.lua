-- hero_equipment_talent.lua: 装备天赋（复杂的 index/talentIndex 计算）
local network = require("network")
local robot = require("robot")
local proto = require("proto")

function execute(r)
    local heroIds = robot.get("heroIdList")
    if not heroIds or type(heroIds) ~= "table" or #heroIds == 0 then
        return 0
    end

    local heroId = heroIds[math.random(#heroIds)]

    -- 从 playerData 中查找天赋状态
    local nextIndex = -1
    local playerData = robot.get("playerData")
    if playerData and playerData.loginHeroData and playerData.loginHeroData.heroData
        and playerData.loginHeroData.heroData.heroList then
        for _, hero in ipairs(playerData.loginHeroData.heroData.heroList) do
            if hero.heroId == heroId and hero.equipTalent then
                for i, val in ipairs(hero.equipTalent) do
                    if val == 0 then
                        nextIndex = i - 1  -- 0-based
                        break
                    end
                end
                break
            end
        end
    end

    if nextIndex <= 0 then
        return 0
    end

    -- 计算 randIndex: random(1, nextIndex) 向下取偶数
    local randIndex = math.random(1, nextIndex)
    randIndex = randIndex - (randIndex % 2)

    local talentIndex = math.random(0, 3)

    local msg = proto.create("Game.HeroEquipmentTalentC2S")
    proto.set_field(msg, "heroId", heroId)
    proto.set_field(msg, "index", {randIndex})
    proto.set_field(msg, "talentIndex", {talentIndex})

    network.send("logic", {cmd=6, act=6}, msg)
    return 0
end
