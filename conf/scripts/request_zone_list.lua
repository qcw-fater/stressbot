-- request_zone_list.lua: HTTP POST /zoneList 获取区服列表
local network = require("network")
local robot = require("robot")
local json = require("json")

function execute(r)
    local account = robot.get("account")
    local version = robot.get("version") or "1.0.0"
    local channel = robot.get("platform") or "1000"
    local code, body = network.http_post("/zoneList", {
        account = account,
        version = version,
        channel = channel
    })
    if code < 0 then
        return 1
    end

    local ok, resp = pcall(json.decode, body)
    if not ok then
        return 0
    end

    -- 尝试从响应中提取逻辑服地址
    if resp and resp.data and resp.data.logicAddress then
        robot.set("logicAddress", resp.data.logicAddress)
    end

    return 0
end
