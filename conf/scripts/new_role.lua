-- new_role.lua: HTTP POST /newRole 创建新角色
-- 需要: account, heroId, session, zoneId, version, channel
-- 响应: {error:0, role:{playerId:123, ...}}
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
    local session = robot.get("session")
    local zoneId  = robot.get("zoneId") or 1
    local version = robot.get("version") or "1.0.0"
    local channel = robot.get("channel") or "test"

    local heroIds = {142, 143, 148}
    local heroId = heroIds[math.random(#heroIds)]

    local authAddr = robot.get("authAddr") or ""
    local url = authAddr .. "/newRole"
    local err, status, body = network.http_request(url, "POST", "form", {
        account = account,
        heroId  = tostring(heroId),
        session = session,
        zoneId  = tostring(zoneId),
        version = version,
        channel = channel
    })

    if err then
        log.error("NewRole HTTP 请求失败: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " heroId=" .. tostring(heroId)
            .. " zoneId=" .. tostring(zoneId)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    if status < 200 or status >= 300 then
        log.error("NewRole HTTP 状态异常: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " heroId=" .. tostring(heroId)
            .. " zoneId=" .. tostring(zoneId)
            .. " status=" .. tostring(status)
            .. " body=" .. body_preview(body))
        return robot.error(54, "NewRole HTTP 状态异常: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " heroId=" .. tostring(heroId)
            .. " zoneId=" .. tostring(zoneId)
            .. " status=" .. tostring(status)
            .. " body=" .. body_preview(body))  -- 54=LUA_SCRIPT_CHECK：脚本断言失败
    end

    local ok, resp = pcall(json.decode, body)
    if not ok or not resp then
        log.error("NewRole JSON 解析失败: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " heroId=" .. tostring(heroId)
            .. " zoneId=" .. tostring(zoneId)            .. " body=" .. body_preview(body))
        return robot.error(54, "NewRole JSON 解析失败: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " heroId=" .. tostring(heroId)
            .. " zoneId=" .. tostring(zoneId)
            .. " body=" .. body_preview(body))  -- 54=LUA_SCRIPT_CHECK：脚本断言失败
    end

    if resp.error and resp.error ~= 0 then
        log.error("NewRole 失败: account=" .. tostring(account)
            .. " heroId=" .. tostring(heroId)
            .. " zoneId=" .. tostring(zoneId)
            .. " error=" .. tostring(resp.error)            .. " body=" .. body_preview(body))
        return robot.error(resp.error, "NewRole 业务失败: account=" .. tostring(account)
            .. " heroId=" .. tostring(heroId)
            .. " zoneId=" .. tostring(zoneId)
            .. " error=" .. tostring(resp.error)
            .. " body=" .. body_preview(body))  -- 透传服务端业务错误码（≥100）
    end

    -- 提取角色信息
    if resp.role then
        local playerId = resp.role.playerId
        if playerId then
            robot.set("roleId", playerId)
            -- 更新 roles 列表
            local roles = robot.get("roles") or {}
            table.insert(roles, resp.role)
            robot.set("roles", roles)
            log.info("NewRole 成功: account=" .. tostring(account)
                .. " heroId=" .. tostring(heroId)
                .. " playerId=" .. tostring(playerId))
        else
            log.error("NewRole 响应中缺少 playerId: account=" .. tostring(account)
                .. " heroId=" .. tostring(heroId)
                .. " zoneId=" .. tostring(zoneId)
                .. " body=" .. body_preview(body))
            return robot.error(54, "NewRole 响应中缺少 playerId: account=" .. tostring(account)
                .. " heroId=" .. tostring(heroId)
                .. " zoneId=" .. tostring(zoneId)
                .. " body=" .. body_preview(body))  -- 54=LUA_SCRIPT_CHECK：脚本断言失败
        end
    else
        log.error("NewRole 响应缺少 role 字段: account=" .. tostring(account)
            .. " heroId=" .. tostring(heroId)
            .. " zoneId=" .. tostring(zoneId)            .. " body=" .. body_preview(body))
        return robot.error(54, "NewRole 响应缺少 role 字段: account=" .. tostring(account)
            .. " heroId=" .. tostring(heroId)
            .. " zoneId=" .. tostring(zoneId)
            .. " body=" .. body_preview(body))  -- 54=LUA_SCRIPT_CHECK：脚本断言失败
    end

    return nil
end
