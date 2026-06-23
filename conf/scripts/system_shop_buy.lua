-- system_shop_buy.lua: 压测用商店购买，从推送数据里随机挑一个商品购买 1 个，失败算正常噪声
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    local shopData = robot.get("systemShopData")
    if type(shopData) ~= "table" or type(shopData.shopData) ~= "table" or #shopData.shopData == 0 then
        return nil
    end

    local shop = shopData.shopData[utils.random_int(#shopData.shopData) + 1]
    local shopId = tonumber(shop.ID) or 0
    if shopId <= 0 then
        return nil
    end

    local groupId = 0
    local shopGroupData = robot.get("systemShopGroupData")
    if type(shopGroupData) == "table" and type(shopGroupData.GroupsId) == "table" then
        groupId = tonumber(shopGroupData.GroupsId[tostring(shopId)]) or 0
    end

    local groupConfigId = nil
    if type(shop.GoodsList) == "table" then
        groupConfigId = tonumber(shop.GoodsList[groupId + 1])
    end

    local group = nil
    if type(shopData.groupData) == "table" then
        for _, item in ipairs(shopData.groupData) do
            if tonumber(item.id) == groupConfigId then
                group = item
                break
            end
        end
    end
    if group == nil or type(group.goodsList) ~= "table" or #group.goodsList == 0 then
        return nil
    end

    local goodsId = tonumber(group.goodsList[utils.random_int(#group.goodsList) + 1]) or 0
    if goodsId <= 0 then
        return nil
    end

    local price = 0
    if type(shopData.goodsList) == "table" then
        for _, goods in ipairs(shopData.goodsList) do
            if tonumber(goods.ID) == goodsId then
                price = tonumber(goods.Price) or 0
                break
            end
        end
    end

    local goodsData = proto.create("Game.BuyGoodsData")
    proto.set_field(goodsData, "shopId", shopId)
    proto.set_field(goodsData, "goodsId", goodsId)
    proto.set_field(goodsData, "buyNumber", 1)
    proto.set_field(goodsData, "costNumber", price)
    proto.set_field(goodsData, "isAdditionalCost", false)
    proto.set_field(goodsData, "groupId", groupId)
    proto.set_field(goodsData, "isContinueBuy", false)

    local msg = proto.create("Game.SystemShopBuyC2S")
    proto.set_field(msg, "goodsList", {goodsData})

    local err = network.tcp_request("logic", {cmd = 35, act = 4}, msg)
    return err  -- nil 成功 / err table 失败
end
