-- smart_buy.lua：按背包剩余格子数决定购买数量（第 11 章主示例）
-- 契约：function execute(r)；return nil = 成功；return robot.error(code, detail) = 失败。
local robot = require("robot")
local proto = require("proto")
local log = require("log")
local network = require("network")
local utils = require("utils")

function execute(r)
    -- 1. 借只读视图看背包（大消息只挑着看，零物化）
    local view = robot.get_view("playerData")
    if view == nil then
        -- 拉角色数据失败（上游动作失败时 state 缺失），本脚本视为正常跳过
        log.warn("playerData 缺失，跳过购买")
        return nil
    end

    local bagSlots = proto.get_field(view, "bagSlots")
    local used = proto.list_size(view, "items")
    local free = bagSlots - used
    if free <= 0 then
        log.info("背包已满: free=" .. tostring(free))
        return nil
    end

    -- 2. 逛商店（tcp_request 第 4 参 s2cProto、第 5 参超时秒）
    local msg = proto.create("arena.ShopListC2S")
    proto.set_field(msg, "category", 0)
    local err, data = network.tcp_request("arena", { cmd = 500, act = 1 }, msg, "arena.ShopListS2C", 5)
    if err ~= nil then
        return err -- 透传失败（err.code 区分框架码/业务码）
    end
    -- 响应 userdata 未改写时原样以 wire 字节存入 state
    robot.set("shopItems", data)

    -- 3. 从商品列表随机挑一个（list_get 下标 1-based）
    local count = proto.list_size(data, "items")
    if count == 0 then
        return nil
    end
    local pick = proto.list_get(data, "items", utils.rand_range(1, count))
    local itemId = proto.get_field(pick, "itemId")

    -- 4. 买 min(free, 3) 个
    local buyCount = math.min(free, 3)
    local buy = proto.create("arena.BuyItemC2S")
    proto.set_field(buy, "itemId", itemId)
    proto.set_field(buy, "count", buyCount)
    local berr, bdata = network.tcp_request("arena", { cmd = 500, act = 2 }, buy, "arena.BuyItemS2C", 5)
    if berr ~= nil then
        return berr
    end

    local orderId = proto.get_field(bdata, "orderId")
    robot.set_path("lastBuy.orderId", orderId)
    return nil
end
