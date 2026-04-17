-- post_login.lua: HTTP POST /login 登录认证服
-- 响应格式: {error:0, session:"...", account:"...", lastZoneId:1, roles:[...], ...}
local network = require("network")
local robot = require("robot")
local json = require("json")
local utils = require("utils")

function execute(r)
    local account = robot.get("account")
    local version = robot.get("version") or "1.0.0"
    local channel = robot.get("channel") or "test"
    local platform = robot.get("platform") or "1000"

    -- 预计算 battleVersion（FNV-1a 哈希）
    robot.set("battleVersion", utils.fnv_hash(version))

    local code, body = network.http_post("/login", {
        account  = account,
        version  = version,
        channel  = channel,
        platform = platform
    })

    if code < 0 then
        utils.log_error("PostLogin HTTP 请求失败: code=" .. tostring(code))
        return 1
    end

    local ok, resp = pcall(json.decode, body)
    if not ok or not resp then
        utils.log_error("PostLogin JSON 解析失败: " .. tostring(body))
        return 1
    end

    -- 检查错误码（error=0 表示成功）
    if resp.error and resp.error ~= 0 then
        utils.log_error("PostLogin 失败: error=" .. tostring(resp.error))
        return 1
    end

    -- 提取 session
    if not resp.session or resp.session == "" then
        utils.log_error("PostLogin 响应缺少 session")
        return 1
    end
    robot.set("session", resp.session)

    -- 存储 lastZoneId（默认 1）
    local zoneId = resp.lastZoneId or 1
    robot.set("zoneId", zoneId)

    -- 存储角色列表（roles 数组，每个元素有 playerId/name/server 等字段）
    local roles = resp.roles or {}
    robot.set("roles", roles)

    utils.log_info("PostLogin 成功: session=" .. tostring(resp.session)
        .. " zoneId=" .. tostring(zoneId)
        .. " 角色数=" .. tostring(#roles))

    return 0
end
