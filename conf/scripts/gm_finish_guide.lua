-- gm_finish_guide.lua: GM 命令完成所有新手引导
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

function execute(r)
    local gm = proto.create("Game.GMEvent")
    proto.set_field(gm, "type", 52)  -- FinishAllNewUserGuide

    local msg = proto.create("Game.GmEventC2S")
    proto.set_field(msg, "GM", gm)

    local code = network.send("logic", {cmd=9, act=1}, msg)
    if code ~= 0 then
        utils.log_error("GMFinishGuide 发送失败")
    else
        utils.log_info("GMFinishGuide 完成")
    end
    return 0
end
