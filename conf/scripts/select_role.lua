-- select_role.lua: HTTP POST /useRole 选择角色
-- 需要: account, id(playerId), session, zoneId, version, platform
-- 响应: {error:0, playerId:123, session:"logic-session", ip:"127.0.0.1", port:9001}
local network = require("network")
local robot = require("robot")
local json = require("json")
local log = require("log")

function execute(r)
    local account  = robot.get("account")
    local session  = robot.get("session")
    local zoneId   = robot.get("zoneId") or 1
    local version  = robot.get("version") or "1.0.0"
    local platform = robot.get("platform") or "1000"

    -- 获取 playerId：优先使用 roleId，否则从 roles 列表取第一个
    local playerId = robot.get("roleId")
    if not playerId then
        local roles = robot.get("roles")
        if roles and type(roles) == "table" and #roles > 0 then
            playerId = roles[1].playerId
        end
    end

    -- 确保 roleId 始终被存储（无论来源是 newRole 还是已有角色列表）
    if playerId then
        robot.set("roleId", playerId)
    end

    if not playerId then
        log.error("SelectRole: 无可用角色")
        return 54, 0, 0  -- 54=LUA_EXIT_CODE：业务前置条件不足
    end

    local authAddr = robot.get("authAddr") or ""
    local code, body, sent, recv = network.http_request(authAddr .. "/useRole", "POST", "form", {
        account  = account,
        id       = tostring(playerId),
        session  = session,
        zoneId   = tostring(zoneId),
        version  = version,
        platform = platform
    })

    if code < 0 then
        log.error("SelectRole HTTP 请求失败: code=" .. tostring(code))
        return 3, sent, recv  -- 3=SEND_FAILED
    end

    local ok, resp = pcall(json.decode, body)
    if not ok or not resp then
        log.error("SelectRole JSON 解析失败")
        return 54, sent, recv
    end

    if resp.error and resp.error ~= 0 then
        log.error("SelectRole 失败: error=" .. tostring(resp.error))
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
        log.info("SelectRole 成功: playerId=" .. tostring(playerId)
            .. " logicAddress=" .. logicAddress)
    else
        log.info("SelectRole 成功: playerId=" .. tostring(playerId)
            .. " (无逻辑服地址)")
    end

    return 0, sent, recv
end
