-- select_hero.lua: 随机选择英雄（用于队伍匹配）
-- 从 state 中读取 heroIdList（由 request_player_data.lua 存储）
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    local heroIds = robot.get("heroIdList")
    if not heroIds or type(heroIds) ~= "table" or #heroIds == 0 then
        heroIds = {101, 102, 103, 104, 105, 106, 107, 108, 109, 110}
    end

    -- 对齐旧工具 utils.RandSilenceSome(heroList, 3)：从英雄列表中无重复地随机选 3 个
    local picked = utils.rand_filter(heroIds, {}, 3)
    if not picked or #picked == 0 then
        picked = {heroIds[math.random(#heroIds)]}
    end

    robot.set("currentHeroIds", picked)

    local msg = proto.create("Game.TeamChangeHeroC2S")
    proto.set_field(msg, "selectHeroes", picked)
    -- 旧工具 SelectSkins 与 SelectHeroes 完全相同（同数组）
    proto.set_field(msg, "selectSkins", picked)

    local code = network.send("logic", {cmd=5, act=14}, msg)
    if code ~= 0 then
        utils.log_error("SelectHero 发送失败")
    end

    return 0
end
