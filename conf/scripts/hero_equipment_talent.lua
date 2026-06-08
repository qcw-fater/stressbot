-- hero_equipment_talent.lua: 装备天赋（复杂的 index/talentIndex 计算）
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    local heroIds = robot.get("heroIdList")
    if not heroIds or type(heroIds) ~= "table" or #heroIds == 0 then
        log.debug("装备英雄天赋跳过: heroIdList 为空")
        return 0
    end

    local heroId = heroIds[math.random(#heroIds)]

    -- 用 get_path 只取 heroList 子树（仅转换这一小块，避免整表转换全量 playerData）
    -- 路径: playerData -> loginHeroData -> HeroData(大写 H) -> heroList
    local nextIndex = -1
    local heroList = robot.get_path("playerData.loginHeroData.HeroData.heroList")
    if heroList then
        for _, hero in ipairs(heroList) do
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
        log.debug("装备英雄天赋跳过: heroId=" .. tostring(heroId)
            .. " nextIndex=" .. tostring(nextIndex)
            .. " reason=无可装备天赋位")
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

    local code = network.tcp_send("logic", {cmd=6, act=6}, msg)
    if code ~= 0 then
        local failCode = code or 3
        log.warn("装备英雄天赋发送失败: service=logic route=6:6 heroId=" .. tostring(heroId)
            .. " randIndex=" .. tostring(randIndex)
            .. " talentIndex=" .. tostring(talentIndex)
            .. " code=" .. tostring(failCode))
        return failCode
    end

    log.debug("装备英雄天赋已发送: heroId=" .. tostring(heroId)
        .. " randIndex=" .. tostring(randIndex)
        .. " talentIndex=" .. tostring(talentIndex))
    return 0
end
