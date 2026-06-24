-- load_progress.lua: 发送加载进度（BattleLoadProgressC2S CMD=4, ACT=7）
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")
local log = require("log")

function execute(r)
    local battleId = robot.get("battleId")
    local fighterIndex = robot.get("fighterIndex")
    local progress = robot.get("loadProgress") or 0
    progress = progress + 20
    if progress > 100 then
        progress = 100
    end
    robot.set("loadProgress", progress)

    local msg = proto.create("Game.BattleLoadProgressC2S")
    proto.set_field(msg, "progress", progress)

    local err = network.tcp_send("battle", {cmd=4, act=7}, msg)
    if err then
        log.warn("LoadProgress 发送失败: progress=" .. tostring(progress)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    if progress >= 100 then
        log.info("LoadProgress 完成: progress=" .. tostring(progress)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex))
    else
        log.debug("LoadProgress: progress=" .. tostring(progress)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex))
    end

    -- 模拟加载间隔
    utils.sleep(500)

    return nil
end
