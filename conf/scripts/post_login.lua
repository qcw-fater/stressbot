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
    local code, body, sent, recv = network.http_request(authAddr .. "/login", "POST", "form", {
        account  = account,
        version  = version,
        channel  = channel,
        platform = platform
    })

    if code < 0 then
        log.error("PostLogin HTTP 请求失败: code=" .. tostring(code) .. " body=" .. body_preview(body))
        return 3, sent, recv  -- 3=SEND_FAILED：HTTP 传输层失败
    end

    if code < 200 or code >= 300 then
        log.error("PostLogin HTTP 状态异常: code=" .. tostring(code) .. " body=" .. body_preview(body))
        return 54, sent, recv  -- 54=LUA_EXIT_CODE：业务层异常
    end

    local ok, resp = pcall(json.decode, body)
    if not ok or not resp then
        log.error("PostLogin JSON 解析失败: " .. body_preview(body))
        return 54, sent, recv  -- 54=LUA_EXIT_CODE：业务层异常
    end

    -- 检查错误码（error=0 表示成功）
    if resp.error and resp.error ~= 0 then
        log.error("PostLogin 失败: error=" .. tostring(resp.error))
        return 54, sent, recv  -- 业务返回非零 error，归 LUA_EXIT_CODE
    end

    -- 提取 session
    if not resp.session or resp.session == "" then
        log.error("PostLogin 响应缺少 session")
        return 54, sent, recv
    end
    robot.set("session", resp.session)

    -- 存储 lastZoneId（默认 1）
    local zoneId = resp.lastZoneId or 1
    robot.set("zoneId", zoneId)

    -- 存储角色列表（roles 数组，每个元素有 playerId/name/server 等字段）
    local roles = resp.roles or {}
    robot.set("roles", roles)

    log.info("PostLogin 成功: session=" .. tostring(resp.session)
        .. " zoneId=" .. tostring(zoneId)
        .. " 角色数=" .. tostring(#roles))

    return 0, sent, recv
end
