-- ranked_member_wait_join.lua: 队员 drain TeamJoin(5:10) 等入队确认
-- body code==0 → 写 Redis members joined，返回 nil；code!=0（入队被拒）或超时 → 返回 err(skip)。
-- teamId 由 TeamJoinS2C 确认写入（成员真正入队后的队伍 id）。
local share = require("share")
local robot = require("robot")
local proto = require("proto")
local network = require("network")
local utils = require("utils")
local log = require("log")

local TEAM_TTL = 600
local WAIT_MS = 15000
local POLL_MS = 250
local PROTO_JOIN = "Game.TeamJoinS2C"

local function waitMarkKey(id) return "ranked:v2:wait:" .. tostring(id) end

-- 返回 joined, failed：joined=收到 code==0；failed=收到 code!=0（被拒）
local function drainJoin(roleId, account)
    local joined = false
    local failed = false
    while true do
        local err, raw = network.try_tcp_listen("logic", {cmd=5, act=10})
        if err and err.code == 31 then break end
        if err then break end
        if raw == nil or raw == "" then break end
        local ok, msg = pcall(proto.parse, PROTO_JOIN, raw)
        if not ok then
            log.warn("drainJoin 解析失败丢弃: err=" .. tostring(msg))
        else
            local codeNum = tonumber(proto.get_field(msg, "code"))
            robot.set("teamJoinCode", codeNum or -1)
            local teamId = proto.get_field(msg, "teamId")
            if teamId then robot.set("teamId", teamId) end  -- int64 雪花 ID 保持字符串原值，勿 tonumber
            robot.set("teamModel", proto.get_field(msg, "model"))
            robot.set("teamGType", proto.get_field(msg, "gType"))
            robot.set("teamModeId", proto.get_field(msg, "modeId"))
            robot.set("teamTsId", proto.get_field(msg, "tsId"))
            if codeNum == 0 then
                joined = true
                local tk = robot.get("rankedTeamKey")
                if tk and tostring(tk) ~= "" then
                    share.hash_set("ranked:v2:team:" .. tostring(tk) .. ":members", tostring(roleId), {
                        playerId = roleId, account = account, role = "member", status = "joined",
                        joinedAtMs = tostring(utils.time_ms()),
                    }, TEAM_TTL)
                end
            else
                failed = true
            end
        end
    end
    return joined, failed
end

function execute(r)
    local roleId = tonumber(robot.get("roleId"))
    local account = robot.get("account") or ""
    if not roleId then
        return robot.error(54, "RankedMemberWaitJoin 缺少 roleId")
    end
    robot.delete("teamJoinCode")
    local waited = 0
    while waited < WAIT_MS do
        local joined, failed = drainJoin(roleId, account)
        if joined then
            log.info("排位队员入队成功: roleId=" .. tostring(roleId) .. " teamId=" .. tostring(robot.get("teamId")))
            return nil
        end
        if failed then
            share.hash_set(waitMarkKey(roleId), "status", "join_failed", 30)
            log.warn("排位队员入队被拒，跳过本轮: roleId=" .. tostring(roleId) .. " code=" .. tostring(robot.get("teamJoinCode")))
            return robot.error(54, "RankedMemberWaitJoin 入队被拒")
        end
        utils.sleep(POLL_MS)
        waited = waited + POLL_MS
    end
    share.hash_set(waitMarkKey(roleId), "status", "join_timeout", 30)
    log.warn("排位队员等入队超时，跳过本轮: roleId=" .. tostring(roleId) .. " code=" .. tostring(robot.get("teamJoinCode")))
    return robot.error(54, "RankedMemberWaitJoin 超时")
end
