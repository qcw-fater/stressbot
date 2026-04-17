-- get_daily_win_box_reward.lua: 领取每日胜场宝箱（repeated int32 ids）
local network = require("network")
local robot = require("robot")
local proto = require("proto")

function execute(r)
    local ids = {1, 4, 8}
    local selectedId = ids[math.random(#ids)]

    local msg = proto.create("Game.MainGetDailyWinBoxRewardC2S")
    proto.set_field(msg, "ids", {selectedId})

    local code = network.send("logic", 2, 66, msg)
    return code
end
