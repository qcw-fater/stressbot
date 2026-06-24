-- hero_activate_talent.lua: 激活英雄天赋（需要找到下一个可激活的天赋位）
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    local heroIds = robot.get("heroIdList")
    if not heroIds or type(heroIds) ~= "table" or #heroIds == 0 then
        log.debug("激活英雄天赋跳过: heroIdList 为空")
        return nil
    end

    local heroId = heroIds[math.random(#heroIds)]

    -- 用 get_path 只取 heroList 子树（仅转换这一小块，避免整表转换全量 playerData）
    -- 路径: playerData -> loginHeroData -> HeroData(大写 H) -> heroList
    local talentIndex = -1
    local heroList = robot.get_path("playerData.loginHeroData.HeroData.heroList")
    if heroList then
        for _, hero in ipairs(heroList) do
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
        log.debug("激活英雄天赋跳过: heroId=" .. tostring(heroId)
            .. " reason=无可激活天赋位")
        return nil
    end

    local msg = proto.create("Game.HeroActivateTalentC2S")
    proto.set_field(msg, "heroId", heroId)
    proto.set_field(msg, "index", talentIndex)

    local err = network.tcp_send("logic", {cmd=6, act=5}, msg)
    if err then
        log.warn("激活英雄天赋发送失败: service=logic route=6:5 heroId=" .. tostring(heroId)
            .. " talentIndex=" .. tostring(talentIndex)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    log.debug("激活英雄天赋已发送: heroId=" .. tostring(heroId)
        .. " talentIndex=" .. tostring(talentIndex))
    return nil
end
