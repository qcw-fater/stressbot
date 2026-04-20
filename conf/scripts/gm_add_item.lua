-- gm_add_item.lua: GM 命令添加道具（对齐旧 Robot game/03_gm.go OnHandleGMAddItem）
local network = require("network")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    -- 添加道具：type=AddItem(4), param={itemId, count, ...}
    -- 2147483647 = math.MaxInt32
    local maxInt32 = 2147483647
    local params = {
        1, maxInt32,       -- 纪念币
        2, maxInt32,       -- 意志结晶
        5, maxInt32,       -- 能量石
        6, maxInt32,       -- 皮肤币
        7, maxInt32,       -- 觉醒道具兑换币
        8, maxInt32,       -- 免费跃之星
        10, maxInt32,      -- 限时阅览券
        14, maxInt32,      -- 特供阅览券
        32, maxInt32,      -- 阅览劵
        33, maxInt32,      -- 特典币
        -- 头像
        7200001, 1,
        7200002, 1,
        7200003, 1,
        7200004, 1,
        -- 头像框
        7300001, 1,
        7300003, 1,
        7300004, 1,
        7300005, 1,
        7300006, 1,
        -- 称号
        7090001, 1,
        7090002, 1,
        7090005, 1,
        -- 表情
        7110001, 1,
        7110002, 1,
        7110003, 1,
        7110004, 1,
        7110005, 1,
        7110006, 1,
        7110007, 1,
        7110008, 1,
        7110009, 1,
        -- 主界面展示背景图
        7120001, 1,
        7120002, 1,
        7120003, 1,
        7120004, 1,
        7120005, 1,
        -- 战令经验
        7040001, 100000,
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
