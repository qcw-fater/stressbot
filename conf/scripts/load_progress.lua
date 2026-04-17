-- load_progress.lua: 发送加载进度（BattleLoadProgressC2S CMD=4, ACT=7）
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    local progress = robot.get("loadProgress") or 0
    progress = progress + 20
    if progress > 100 then
        progress = 100
    end
    robot.set("loadProgress", progress)

    local msg = proto.create("Game.BattleLoadProgressC2S")
    proto.set_field(msg, "progress", progress)

    local code = network.send("battle", 4, 7, msg)
    utils.log_info("LoadProgress: " .. progress .. "% code=" .. tostring(code))

    -- 模拟加载间隔
    utils.sleep(500)

    return 0
end
