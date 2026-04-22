-- gm_add_hero.lua: GM 命令添加英雄（对齐旧 Robot game/03_gm.go OnHandleGMAddHero）
local network = require("network")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    -- 添加英雄：type=AddItem(4), param={heroId, count, ...}
    local heroIds = {
        109, 110, 113, 118, 122, 123, 124, 125,
        130, 138, 140, 142, 143, 148, 150, 153,
        156, 158, 159, 160,
    }
    local params = {}
    for _, heroId in ipairs(heroIds) do
        table.insert(params, heroId)
        table.insert(params, 5)
    end

    local gm = proto.create("Game.GMEvent")
    proto.set_field(gm, "type", 4)  -- AddItem
    proto.set_field(gm, "param", params)

    local msg = proto.create("Game.GmEventC2S")
    proto.set_field(msg, "GM", gm)

    local code = network.send("logic", {cmd=9, act=1}, msg)
    if code ~= 0 then
        utils.log_error("GMAddHero 发送失败")
    else
        utils.log_info("GMAddHero 完成: " .. #heroIds .. " 个英雄")
    end
    return 0
end
