-- load_progress.lua: 发送加载进度（BattleLoadProgressC2S CMD=4, ACT=7）
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")
local log = require("log")

function execute(r)
    local progress = robot.get("loadProgress") or 0
    progress = progress + 20
    if progress > 100 then
        progress = 100
    end
    robot.set("loadProgress", progress)

    local msg = proto.create("Game.BattleLoadProgressC2S")
    proto.set_field(msg, "progress", progress)

    local code, sent = network.tcp_send("battle", {cmd=4, act=7}, msg)
    if code ~= 0 then
        log.warn("LoadProgress 发送失败: code=" .. tostring(code))
    else
        log.info("LoadProgress: " .. progress .. "%")
    end

    -- 模拟加载间隔
    utils.sleep(500)

    return 0, sent, 0
end
