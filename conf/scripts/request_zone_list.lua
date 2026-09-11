-- request_zone_list.lua: HTTP POST /zoneList 获取区服列表
-- Action 脚本统一返回约定：return nil（成功）/ err table（失败）
-- send/recv WireBytes 由 network API 自动计入当前脚本 Context。
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
    local err, status, body = network.http_request(url, "POST", "form", {
        account = account,
        version = version,
        channel = channel
    })
    if err then
        log.error("RequestZoneList HTTP 请求失败: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    if status < 200 or status >= 300 then
        log.error("RequestZoneList HTTP 状态异常: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " status=" .. tostring(status)
            .. " body=" .. body_preview(body))
        return robot.error(54, "RequestZoneList HTTP 状态异常: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " status=" .. tostring(status)
            .. " body=" .. body_preview(body))  -- 54=LUA_SCRIPT_CHECK：脚本断言失败
    end

    local ok, resp = pcall(json.decode, body)
    if not ok or not resp then
        log.error("RequestZoneList JSON 解析失败: account=" .. tostring(account)
            .. " url=" .. tostring(url)            .. " body=" .. body_preview(body))
        return robot.error(54, "RequestZoneList JSON 解析失败: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " body=" .. body_preview(body))  -- 54=LUA_SCRIPT_CHECK：脚本断言失败
    end

    -- 检查错误码（error=0 表示成功）
    if resp.error and resp.error ~= 0 then
        log.error("RequestZoneList 失败: account=" .. tostring(account)
            .. " error=" .. tostring(resp.error)            .. " body=" .. body_preview(body))
        return robot.error(resp.error, "RequestZoneList 业务失败: account=" .. tostring(account)
            .. " error=" .. tostring(resp.error)
            .. " body=" .. body_preview(body))  -- 透传服务端业务错误码（≥100）
    end

    -- 提取 zoneList
    if not resp.areas or resp.areas == "" or #resp.areas == 0 then
        log.error("RequestZoneList 响应缺少 areas: account=" .. tostring(account) .. " body=" .. body_preview(body))
        return robot.error(54, "RequestZoneList 响应缺少 areas: account=" .. tostring(account)
            .. " body=" .. body_preview(body))  -- 54=LUA_SCRIPT_CHECK：脚本断言失败
    end
    robot.set("zoneList", resp.areas)

    log.info("RequestZoneList 成功: account=" .. tostring(account) .. " 区服数=" .. tostring(#resp.areas))

    return nil
end
