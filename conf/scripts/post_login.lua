-- post_login.lua: HTTP POST /login 登录认证服
-- 响应格式: {error:0, session:"...", account:"...", lastZoneId:1, roles:[...], ...}
local network = require("network")
local robot = require("robot")
local json = require("json")
local utils = require("utils")
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
    local channel = robot.get("channel") or "test"
    local platform = robot.get("platform") or "1000"

    -- 预计算 battleVersion（FNV-1a 哈希）
    robot.set("battleVersion", utils.fnv_hash(version))

    local authAddr = robot.get("authAddr") or ""
    local url = authAddr .. "/login"
    local err, status, body = network.http_request(url, "POST", "form", {
        account  = account,
        version  = version,
        channel  = channel,
        platform = platform
    })

    if err then
        log.error("PostLogin HTTP 请求失败: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    if status < 200 or status >= 300 then
        log.error("PostLogin HTTP 状态异常: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " status=" .. tostring(status)
            .. " body=" .. body_preview(body))
        return robot.error(54, "PostLogin HTTP 状态异常: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " status=" .. tostring(status)
            .. " body=" .. body_preview(body))  -- 54=LUA_SCRIPT_CHECK：脚本断言失败
    end

    local ok, resp = pcall(json.decode, body)
    if not ok or not resp then
        log.error("PostLogin JSON 解析失败: account=" .. tostring(account)
            .. " url=" .. tostring(url)            .. " body=" .. body_preview(body))
        return robot.error(54, "PostLogin JSON 解析失败: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " body=" .. body_preview(body))  -- 54=LUA_SCRIPT_CHECK：脚本断言失败
    end

    -- 检查错误码（error=0 表示成功）
    if resp.error and resp.error ~= 0 then
        log.error("PostLogin 失败: account=" .. tostring(account)
            .. " error=" .. tostring(resp.error)            .. " body=" .. body_preview(body))
        return robot.error(resp.error, "PostLogin 业务失败: account=" .. tostring(account)
            .. " error=" .. tostring(resp.error)
            .. " body=" .. body_preview(body))  -- 透传服务端业务错误码（≥100）
    end

    -- 提取 session
    if not resp.session or resp.session == "" then
        log.error("PostLogin 响应缺少 session: account=" .. tostring(account)            .. " body=" .. body_preview(body))
        return robot.error(54, "PostLogin 响应缺少 session: account=" .. tostring(account)
            .. " body=" .. body_preview(body))  -- 54=LUA_SCRIPT_CHECK：脚本断言失败
    end
    robot.set("session", resp.session)

    -- 存储 lastZoneId（默认 1）
    local zoneId = resp.lastZoneId or 1
    robot.set("zoneId", zoneId)

    -- 存储角色列表（roles 数组，每个元素有 playerId/name/server 等字段）
    local roles = resp.roles or {}
    robot.set("roles", roles)

    log.info("PostLogin 成功: account=" .. tostring(account)
        .. " session=" .. tostring(resp.session)
        .. " zoneId=" .. tostring(zoneId)
        .. " 角色数=" .. tostring(#roles))

    return nil
end
