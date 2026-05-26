-- request_zone_list.lua: HTTP POST /zoneList 获取区服列表
-- Action 脚本统一返回约定：return code, send_bytes, recv_bytes
-- send/recv 由 lua API 第 3、4 个返回值给出（详见 script.RunActionScript 注释）。
local network = require("network")
local robot = require("robot")
local json = require("json")

function execute(r)
    local account = robot.get("account")
    local version = robot.get("version") or "1.0.0"
    local channel = robot.get("platform") or "1000"
    local authAddr = robot.get("authAddr") or ""
    local code, body, sent, recv = network.http_request(authAddr .. "/zoneList", "POST", "form", {
        account = account,
        version = version,
        channel = channel
    })
    if code < 0 then
        return 3, sent, recv  -- 3=SEND_FAILED：HTTP 传输层失败
    end

    local ok, resp = pcall(json.decode, body)
    if not ok then
        return 0, sent, recv
    end

    -- 尝试从响应中提取逻辑服地址
    if resp and resp.data and resp.data.logicAddress then
        robot.set("logicAddress", resp.data.logicAddress)
    end

    return 0, sent, recv
end
