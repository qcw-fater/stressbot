-- request_outside_notice.lua: HTTP POST /outsideNotice
local network = require("network")
local robot = require("robot")

function execute(r)
    local account = robot.get("account")
    local channel = robot.get("platform") or "1000"
    local _, _, sent, recv = network.http_post("/outsideNotice", {
        account = account,
        channel = channel
    })
    return 0, sent, recv
end
