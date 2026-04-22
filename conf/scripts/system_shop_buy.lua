-- system_shop_buy.lua: 购买商店物品（repeated BuyGoodsData 嵌套消息）
local network = require("network")
local robot = require("robot")
local proto = require("proto")

function execute(r)
    -- 商品列表: {goodsId, costNumber}
    local goodsPool = {
        {201005, 10},
        {201006, 50},
        {201008, 50},
        {201009, 150},
        {201049, 150},
        {201050, 300},
        {201051, 1000},
        {201052, 250},
        {201053, 130}
    }

    local pick = goodsPool[math.random(#goodsPool)]

    -- 构建嵌套消息
    local buyData = proto.create("Game.BuyGoodsData")
    proto.set_field(buyData, "shopId", 1)
    proto.set_field(buyData, "goodsId", pick[1])
    proto.set_field(buyData, "buyNumber", 1)
    proto.set_field(buyData, "costNumber", pick[2])

    local msg = proto.create("Game.SystemShopBuyC2S")
    proto.set_field(msg, "goodsList", {buyData})

    network.send("logic", {cmd=35, act=4}, msg)
    return 0
end
