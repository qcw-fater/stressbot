-- request_player_data.lua: 发送 MainLoadOK(CMD=2,ACT=16) 并等待 LoginPlayerDataS2C(CMD=1,ACT=2)
-- 使用 request_wait 实现跨 CMD 的请求-响应模式
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    -- 发送 MainLoadOkC2S (CMD=2, ACT=16)，等待 LoginPlayerDataS2C (CMD=1, ACT=2)
    local msg = proto.create("Game.MainLoadOkC2S")
    local code, resp = network.request_wait("logic", {cmd=2, act=16}, msg, {cmd=1, act=2}, "Game.LoginPlayerDataS2C")

    if code ~= 0 then
        utils.log_error("RequestPlayerData: 请求失败 code=" .. tostring(code) .. " detail=" .. tostring(resp))
        return 1
    end

    if not resp then
        utils.log_error("RequestPlayerData: 响应为空")
        return 1
    end

    -- 存储完整玩家数据
    local fieldMap = proto.get_field_map(resp)
    robot.set("playerData", fieldMap)

    -- 提取英雄 ID 列表
    -- 路径: loginHeroData -> HeroData -> heroList -> [].heroId
    local heroIds = {}
    local loginHero = fieldMap.loginHeroData
    if loginHero then
        local heroGetData = loginHero.HeroData
        if heroGetData and heroGetData.heroList then
            for _, hero in ipairs(heroGetData.heroList) do
                if hero.possess then
                    table.insert(heroIds, hero.heroId)
                end
            end
        end
    end

    -- 存储英雄列表（如果为空使用默认列表）
    if #heroIds == 0 then
        heroIds = {101, 102, 103, 104, 105, 106, 107, 108, 109, 110}
    end
    robot.set("heroIdList", heroIds)

    utils.log_info("RequestPlayerData: 已存储 " .. #heroIds .. " 个英雄ID")
    return 0
end
