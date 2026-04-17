-- gm_add_hero_cup.lua: GM 命令提升英雄杯数
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    -- 添加英雄熟练度：type=AddHeroProficiency(13), param={heroId, cup}
    local gm = proto.create("Game.GMEvent")
    proto.set_field(gm, "type", 13)  -- AddHeroProficiency
    proto.set_field(gm, "param", {109, 1000})

    local msg = proto.create("Game.GmEventC2S")
    proto.set_field(msg, "GM", gm)

    local code = network.send("logic", 9, 1, msg)
    if code ~= 0 then
        utils.log_error("GMAddHeroCup 发送失败")
    else
        utils.log_info("GMAddHeroCup 完成")
    end
    return 0
end
