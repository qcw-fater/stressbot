-- request_player_data.lua: 发送 MainLoadOK(CMD=2,ACT=16) 并等待 LoginPlayerDataS2C(CMD=1,ACT=2)
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    -- 发送 MainLoadOkC2S (CMD=2, ACT=16)
    local msg = proto.create("Game.MainLoadOkC2S")
    local _, sent = network.tcp_send("logic", {cmd=2, act=16}, msg)

    -- 等待 LoginPlayerDataS2C (CMD=1, ACT=2)
    local resp, recv = network.wait_listen("logic", {cmd=1, act=2}, "Game.LoginPlayerDataS2C", 30)

    if not resp then
        log.error("RequestPlayerData: 响应为空")
        return 1, sent, recv
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

    log.info("RequestPlayerData: 已存储 " .. #heroIds .. " 个英雄ID")
    return 0, sent, recv
end
