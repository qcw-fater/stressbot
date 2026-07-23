-- on_hero_update.lua: 英雄数据更新推送回调
-- 服务器在获取/变更英雄后下发 HeroUpdateS2C（heroDta = 单个英雄），
-- 在本地英雄列表 playerData.loginHeroData.HeroData.heroList 中按 heroId 原地替换。
local robot = require("robot")
local log = require("log")

local HERO_LIST_PATH = "playerData.loginHeroData.HeroData.heroList"

function on_message(r, msg)
    if not msg then return end

    local ok, err = pcall(function()
        if type(msg) ~= "table" or type(msg.heroDta) ~= "table" then
            return
        end
        -- heroId 是 int32，值恒在 Lua 安全整数范围内（protoScalarToLua 转为 number），无需 to_number。
        local newHeroId = msg.heroDta.heroId
        if newHeroId == nil then return end

        local heroList = robot.get_path(HERO_LIST_PATH)
        if type(heroList) ~= "table" then
            return
        end

        for i, hero in ipairs(heroList) do
            if type(hero) == "table" and hero.heroId == newHeroId then
                heroList[i] = msg.heroDta
                robot.set_path(HERO_LIST_PATH, heroList)
                log.debug("英雄数据更新: heroId=" .. tostring(newHeroId))
                return
            end
        end
    end)
    if not ok then
        log.warn("英雄数据更新推送解析失败: " .. tostring(err))
    end
end
