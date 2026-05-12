-- request_notice.lua: HTTP POST /notice
local network = require("network")
local robot = require("robot")

function execute(r)
    local account = robot.get("account")
    local channel = robot.get("platform") or "1000"
    local authAddr = robot.get("authAddr") or ""
    local _, _, sent, recv = network.http_request(authAddr .. "/notice", "POST", "form", {
        account = account,
        channel = channel
    })
    return 0, sent, recv
end
