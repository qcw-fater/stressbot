-- system_shop_buy.lua: 压测用商店购买，从推送数据里随机挑一个商品购买 1 个，失败算正常噪声
--
-- 消费方式说明（get_view 范例脚本）：
-- systemShopData 是大广播消息（wire 留存），且本脚本只读不改——用 robot.get_view
-- 借只读视图，proto.list_size/list_get/iter_list/get_field 按需窄读，
-- 不整树物化（robot.get 整读一次 ≈ 8ms + 1.2MB 分配，视图读是微秒/KB 级）。
-- 需要整表加工或修改时才用 robot.get（见 AGENTS.md「get 与 get_view 的使用边界」）。
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    local shopView = robot.get_view("systemShopData")
    if shopView == nil then
        return nil
    end
    local shopCount = proto.list_size(shopView, "shopData")
    if shopCount == 0 then
        return nil
    end

    local shop = proto.list_get(shopView, "shopData", utils.random_int(shopCount) + 1)
    local shopId = tonumber(proto.get_field(shop, "ID")) or 0
    if shopId <= 0 then
        return nil
    end

    local groupId = 0
    local groupView = robot.get_view("systemShopGroupData")
    if groupView ~= nil then
        -- GroupsId 是 map 字段：map 不支持路径下钻，get_field 取整个 map（条目级小表）。
        local groupsId = proto.get_field(groupView, "GroupsId")
        if type(groupsId) == "table" then
            groupId = tonumber(groupsId[tostring(shopId)]) or 0
        end
    end

    local groupConfigId = tonumber(proto.list_get(shop, "GoodsList", groupId + 1))

    -- 顺序扫描 groupData 找匹配组：游标迭代零物化，元素是子视图，只读命中字段。
    local group = nil
    for _, item in proto.iter_list(shopView, "groupData") do
        if tonumber(proto.get_field(item, "id")) == groupConfigId then
            group = item
            break
        end
    end
    if group == nil then
        return nil
    end
    local goodsCount = proto.list_size(group, "goodsList")
    if goodsCount == 0 then
        return nil
    end

    local goodsId = tonumber(proto.list_get(group, "goodsList", utils.random_int(goodsCount) + 1)) or 0
    if goodsId <= 0 then
        return nil
    end

    local price = 0
    for _, goods in proto.iter_list(shopView, "goodsList") do
        if tonumber(proto.get_field(goods, "ID")) == goodsId then
            price = tonumber(proto.get_field(goods, "Price")) or 0
            break
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
