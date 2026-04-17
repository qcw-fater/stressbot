-- lottery_lucky_draw.lua: 抽卡（从 lotteryInfoList 中随机选 pool）
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    local lotteryInfoList = robot.get("lotteryInfoList")

    -- 构建 pool 列表
    local pools = {}
    if lotteryInfoList and type(lotteryInfoList) == "table" then
        for _, info in ipairs(lotteryInfoList) do
            if info.lotteryPoolId and info.lotteryPoolId ~= 101 then
                table.insert(pools, info.lotteryPoolId)
            end
        end
    end

    -- 无可用 pool 时使用默认值
    if #pools == 0 then
        pools = {1, 2}
    end

    local poolId = pools[math.random(#pools)]

    local msg = proto.create("Game.LotteryLuckyDrawC2S")
    proto.set_field(msg, "lotteryType", 2)        -- LOT_MORE
    proto.set_field(msg, "lotteryPoolId", poolId)
    proto.set_field(msg, "lotteryConsumeType", 1)  -- LCT_NORMAL

    network.send("logic", 23, 1, msg)
    return 0
end
