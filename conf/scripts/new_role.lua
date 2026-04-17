-- new_role.lua: HTTP POST /newRole 创建新角色
-- 需要: account, heroId, session, zoneId, version, channel
-- 响应: {error:0, role:{playerId:123, ...}}
local network = require("network")
local robot = require("robot")
local json = require("json")
local utils = require("utils")

function execute(r)
    local account = robot.get("account")
    local session = robot.get("session")
    local zoneId  = robot.get("zoneId") or 1
    local version = robot.get("version") or "1.0.0"
    local channel = robot.get("channel") or "test"

    local heroIds = {142, 143, 148}
    local heroId = heroIds[math.random(#heroIds)]

    local code, body = network.http_post("/newRole", {
        account = account,
        heroId  = tostring(heroId),
        session = session,
        zoneId  = tostring(zoneId),
        version = version,
        channel = channel
    })

    if code < 0 then
        utils.log_error("NewRole HTTP 请求失败: code=" .. tostring(code))
        return 1
    end

    local ok, resp = pcall(json.decode, body)
    if not ok or not resp then
        utils.log_error("NewRole JSON 解析失败")
        return 1
    end

    if resp.error and resp.error ~= 0 then
        utils.log_error("NewRole 失败: error=" .. tostring(resp.error))
        return 1
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
            utils.log_info("NewRole 成功: playerId=" .. tostring(playerId))
        else
            utils.log_error("NewRole 响应中缺少 playerId")
            return 1
        end
    else
        utils.log_error("NewRole 响应缺少 role 字段")
        return 1
    end

    return 0
end
