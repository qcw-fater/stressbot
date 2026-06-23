-- request_notice.lua: HTTP POST /notice
local network = require("network")
local robot = require("robot")
local log = require("log")

local function body_preview(body)
    local text = tostring(body or "")
    if #text > 1000 then
        return string.sub(text, 1, 1000) .. "..."
    end
    return text
end

function execute(r)
    local account = robot.get("account")
    local channel = robot.get("platform") or "1000"
    local authAddr = robot.get("authAddr") or ""
    local url = authAddr .. "/notice"
    local err, status, body = network.http_request(url, "POST", "form", {
        account = account,
        channel = channel
    })

    if err then
        log.warn("RequestNotice HTTP 请求失败，不阻断流程: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return nil
    end

    if status < 200 or status >= 300 then
        log.warn("RequestNotice HTTP 状态异常，不阻断流程: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " status=" .. tostring(status)
            .. " body=" .. body_preview(body))
    end

    return nil
end
