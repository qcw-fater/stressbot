-- select_role.lua: HTTP POST /useRole 选择角色
-- 需要: account, id(playerId), session, zoneId, version, platform
-- 响应: {error:0, playerId:123, session:"logic-session", ip:"127.0.0.1", port:9001}
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
    local account  = robot.get("account")
    local session  = robot.get("session")
    local zoneId   = robot.get("zoneId") or 1
    local version  = robot.get("version") or "1.0.0"
    local platform = robot.get("platform") or "1000"

    -- 获取 playerId：优先使用 roleId，否则从 roles 列表取第一个
    local roles = robot.get("roles")
    local roleCount = 0
    if roles and type(roles) == "table" then
        roleCount = #roles
    end

    local playerId = robot.get("roleId")
    if not playerId and roleCount > 0 then
        playerId = roles[1].playerId
    end

    -- 确保 roleId 始终被存储（无论来源是 newRole 还是已有角色列表）
    if playerId then
        robot.set("roleId", playerId)
    end

    if not playerId then
        log.error("SelectRole 无可用角色: account=" .. tostring(account)
            .. " zoneId=" .. tostring(zoneId)
            .. " roleCount=" .. tostring(roleCount))
        return 54, 0, 0  -- 54=LUA_EXIT_CODE：业务前置条件不足
    end

    local authAddr = robot.get("authAddr") or ""
    local url = authAddr .. "/useRole"
    local code, body, sent, recv = network.http_request(url, "POST", "form", {
        account  = account,
        id       = tostring(playerId),
        session  = session,
        zoneId   = tostring(zoneId),
        version  = version,
        platform = platform
    })

    if code < 0 then
        log.error("SelectRole HTTP 请求失败: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " playerId=" .. tostring(playerId)
            .. " zoneId=" .. tostring(zoneId)
            .. " code=" .. tostring(code)
            .. " sent=" .. tostring(sent)
            .. " recv=" .. tostring(recv)
            .. " body=" .. body_preview(body))
        return 3, sent, recv  -- 3=SEND_FAILED
    end

    if code < 200 or code >= 300 then
        log.error("SelectRole HTTP 状态异常: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " playerId=" .. tostring(playerId)
            .. " zoneId=" .. tostring(zoneId)
            .. " status=" .. tostring(code)
            .. " sent=" .. tostring(sent)
            .. " recv=" .. tostring(recv)
            .. " body=" .. body_preview(body))
        return 54, sent, recv
    end

    local ok, resp = pcall(json.decode, body)
    if not ok or not resp then
        log.error("SelectRole JSON 解析失败: account=" .. tostring(account)
            .. " url=" .. tostring(url)
            .. " playerId=" .. tostring(playerId)
            .. " zoneId=" .. tostring(zoneId)
            .. " sent=" .. tostring(sent)
            .. " recv=" .. tostring(recv)
            .. " body=" .. body_preview(body))
        return 54, sent, recv
    end

    if resp.error and resp.error ~= 0 then
        log.error("SelectRole 失败: account=" .. tostring(account)
            .. " playerId=" .. tostring(playerId)
            .. " zoneId=" .. tostring(zoneId)
            .. " error=" .. tostring(resp.error)
            .. " sent=" .. tostring(sent)
            .. " recv=" .. tostring(recv)
            .. " body=" .. body_preview(body))
        return 54, sent, recv
    end

    -- 更新 logicSession
    if resp.session then
        robot.set("session", resp.session)
    end

    -- 构建 logicAddress: ip:port
    if resp.ip and resp.port then
        local logicAddress = resp.ip .. ":" .. tostring(resp.port)
        robot.set("logicAddress", logicAddress)
        log.info("SelectRole 成功: account=" .. tostring(account)
            .. " playerId=" .. tostring(playerId)
            .. " logicAddress=" .. logicAddress)
    else
        log.info("SelectRole 成功: account=" .. tostring(account)
            .. " playerId=" .. tostring(playerId)
            .. " (无逻辑服地址) body=" .. body_preview(body))
    end

    return 0, sent, recv
end
