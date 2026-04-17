-- select_role.lua: HTTP POST /useRole 选择角色
-- 需要: account, id(playerId), session, zoneId, version, platform
-- 响应: {error:0, playerId:123, session:"logic-session", ip:"127.0.0.1", port:9001}
local network = require("network")
local robot = require("robot")
local json = require("json")
local utils = require("utils")

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
        utils.log_error("SelectRole: 无可用角色")
        return 1
    end

    local code, body = network.http_post("/useRole", {
        account  = account,
        id       = tostring(playerId),
        session  = session,
        zoneId   = tostring(zoneId),
        version  = version,
        platform = platform
    })

    if code < 0 then
        utils.log_error("SelectRole HTTP 请求失败: code=" .. tostring(code))
        return 1
    end

    local ok, resp = pcall(json.decode, body)
    if not ok or not resp then
        utils.log_error("SelectRole JSON 解析失败")
        return 1
    end

    if resp.error and resp.error ~= 0 then
        utils.log_error("SelectRole 失败: error=" .. tostring(resp.error))
        return 1
    end

    -- 更新 logicSession
    if resp.session then
        robot.set("session", resp.session)
    end

    -- 构建 logicAddress: ip:port
    if resp.ip and resp.port then
        local logicAddress = resp.ip .. ":" .. tostring(resp.port)
        robot.set("logicAddress", logicAddress)
        utils.log_info("SelectRole 成功: playerId=" .. tostring(playerId)
            .. " logicAddress=" .. logicAddress)
    else
        utils.log_info("SelectRole 成功: playerId=" .. tostring(playerId)
            .. " (无逻辑服地址)")
    end

    return 0
end
