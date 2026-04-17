-- report_battle_delay.lua: 上报战斗延迟（repeated BattleDelay 字段）
local network = require("network")
local robot = require("robot")
local proto = require("proto")

function execute(r)
    local msg = proto.create("Game.MainBattleDelayC2S")

    -- 构建两条延迟记录（BattleDelay 字段名遵循 proto 定义的大小写）
    local delay1 = proto.create("Game.BattleDelay")
    proto.set_field(delay1, "BattleArea", "ap.hongkong")
    proto.set_field(delay1, "DelayTime", math.random(30, 100))
    proto.set_field(delay1, "PingSendCnt", 10)
    proto.set_field(delay1, "PingRecvCnt", 10)

    local delay2 = proto.create("Game.BattleDelay")
    proto.set_field(delay2, "BattleArea", "ap.taibei")
    proto.set_field(delay2, "DelayTime", math.random(30, 100))
    proto.set_field(delay2, "PingSendCnt", 10)
    proto.set_field(delay2, "PingRecvCnt", 10)

    proto.set_field(msg, "battleDelayList", {delay1, delay2})
    network.send("logic", 2, 29, msg)
    return 0
end
