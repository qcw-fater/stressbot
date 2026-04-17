-- gm_add_item.lua: GM 命令添加道具
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    -- 添加道具：type=AddItem(4), param={itemId, count, ...}
    local params = {
        1, 999999,   -- 纪念币
        2, 999999,   -- 意志结晶
        5, 999999,   -- 能量石
        6, 999999,   -- 皮肤币
        7, 999999,   -- 觉醒道具兑换币
        8, 999999    -- 免费跃之星
    }

    local gm = proto.create("Game.GMEvent")
    proto.set_field(gm, "type", 4)  -- AddItem
    proto.set_field(gm, "param", params)

    local msg = proto.create("Game.GmEventC2S")
    proto.set_field(msg, "GM", gm)

    local code = network.send("logic", 9, 1, msg)
    if code ~= 0 then
        utils.log_error("GMAddItem 发送失败")
    else
        utils.log_info("GMAddItem 完成")
    end
    return 0
end
