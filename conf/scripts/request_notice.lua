-- request_notice.lua: HTTP POST /notice
local network = require("network")
local robot = require("robot")

function execute(r)
    local account = robot.get("account")
    local channel = robot.get("platform") or "1000"
    local _, _, sent, recv = network.http_post("/notice", {
        account = account,
        channel = channel
    })
    return 0, sent, recv
end
