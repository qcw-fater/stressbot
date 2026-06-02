-- request_zone_list.lua: HTTP POST /zoneList 获取区服列表
-- Action 脚本统一返回约定：return code, send_bytes, recv_bytes
-- send/recv 由 lua API 第 3、4 个返回值给出（详见 script.RunActionScript 注释）。
local network = require("network")
local robot = require("robot")
local json = require("json")
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
    local version = robot.get("version") or "1.0.0"
    local channel = robot.get("platform") or "1000"
    local authAddr = robot.get("authAddr") or ""
    local url = authAddr .. "/zoneList"
    local code, body, sent, recv = network.http_request(url, "POST", "form", {
        account = account,
        version = version,
        channel = channel
    })
    if code < 0 then
        log.error("RequestZoneList HTTP 请求失败: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " code=" .. tostring(code)
            .. " sent=" .. tostring(sent)
            .. " recv=" .. tostring(recv)
            .. " body=" .. body_preview(body))
        return 3, sent, recv  -- 3=SEND_FAILED：HTTP 传输层失败
    end

    if code < 200 or code >= 300 then
        log.error("RequestZoneList HTTP 状态异常: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " status=" .. tostring(code)
            .. " sent=" .. tostring(sent)
            .. " recv=" .. tostring(recv)
            .. " body=" .. body_preview(body))
        return 54, sent, recv
    end

    return 0, sent, recv
end
