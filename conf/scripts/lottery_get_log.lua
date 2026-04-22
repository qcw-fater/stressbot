-- lottery_get_log.lua: 获取抽卡记录（从 lotteryInfoList 中随机选 pool）
local network = require("network")
local robot = require("robot")
local proto = require("proto")

function execute(r)
    local lotteryInfoList = robot.get("lotteryInfoList")

    local pools = {}
    if lotteryInfoList and type(lotteryInfoList) == "table" then
        for _, info in ipairs(lotteryInfoList) do
            if info.lotteryPoolId and info.lotteryPoolId ~= 101 then
                table.insert(pools, info.lotteryPoolId)
            end
        end
    end

    if #pools == 0 then
        pools = {1, 2}
    end

    local poolId = pools[math.random(#pools)]

    local msg = proto.create("Game.LotteryGetLogC2S")
    proto.set_field(msg, "lotteryPoolId", poolId)

    network.send("logic", {cmd=23, act=4}, msg)
    return 0
end
