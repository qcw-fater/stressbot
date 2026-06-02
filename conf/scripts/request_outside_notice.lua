-- request_outside_notice.lua: HTTP POST /outsideNotice
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
    local url = authAddr .. "/outsideNotice"
    local code, body, sent, recv = network.http_request(url, "POST", "form", {
        account = account,
        channel = channel
    })

    if code < 0 then
        log.warn("RequestOutsideNotice HTTP 请求失败，不阻断流程: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " code=" .. tostring(code)
            .. " sent=" .. tostring(sent)
            .. " recv=" .. tostring(recv)
            .. " body=" .. body_preview(body))
        return 0, sent, recv
    end

    if code < 200 or code >= 300 then
        log.warn("RequestOutsideNotice HTTP 状态异常，不阻断流程: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " status=" .. tostring(code)
            .. " sent=" .. tostring(sent)
            .. " recv=" .. tostring(recv)
            .. " body=" .. body_preview(body))
    end

    return 0, sent, recv
end
