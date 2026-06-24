-- guild_drain.lua: 消费社团推送缓存队列（v2 listen 迁移，取代已下线的 listen 脚本回调）
-- 在 guildCheckFlow 入口每轮调用，drain 4 个社团推送（21:15/21:16/21:14/21:13），
-- 维护本地社团状态（playerData.guildInfo / currentGuildInfo 等），供 has_guild /
-- is_guild_leader 等条件脚本读到最新。逻辑逐条复用原 listen 脚本（含 playerId==roleId 过滤）。
local robot = require("robot")
local network = require("network")
local proto = require("proto")
local log = require("log")

local function to_number(v)
    if v == nil then return nil end
    if type(v) == "number" then return v end
    return tonumber(v)
end

-- drain 一条 route 的所有缓存消息；proto.parse 失败会 RaiseError，必须 pcall。
local function drainRoute(route, protoName, handler)
    while true do
        local err, raw = network.try_tcp_listen("logic", route)
        if err and err.code == 31 then break end   -- 队列空
        if err then break end    -- 其它错误留待下轮
        if raw == nil or raw == "" then break end

        local ok, msg = pcall(proto.parse, protoName, raw)
        if not ok then
            log.warn("guild_drain: 解析失败丢弃 route=" .. tostring(route.cmd) .. ":" .. tostring(route.act)
                .. " err=" .. tostring(msg))
        else
            local fm = proto.get_field_map(msg)
            if fm then handler(fm) end
        end
    end
end

local function clearGuildState(roleId)
    robot.set_path("playerData.guildInfo.guildId", 0)
    robot.set_path("playerData.guildInfo.guildGamePlay", nil)
    robot.set_path("playerData.guildInfo.baseInfo", nil)
    robot.set_path("playerData.guildInfo.baseSetting", nil)
    robot.set_path("playerData.guildInfo.mydata", nil)
    robot.set_path("playerData.guildInfo.routeData", nil)
    robot.set("currentGuildInfo", nil)
    robot.set("guildMembers", nil)
    robot.set("guildApplyList", nil)
    log.info("guild_drain: 自己离开/被移出社团，清理本地状态 roleId=" .. tostring(roleId))
end

-- 21:15 加入社团通知（推送含 data = 当前玩家入会）
local function handleJoin(fm)
    if type(fm.data) == "table" then
        robot.set_path("playerData.guildInfo", fm.data)
        log.info("guild_drain: 收到加入社团通知 guildId=" .. tostring(fm.data.guildId))
    end
end

-- 21:16 被踢/退出通知（playerId==自己才清理）
local function handleKick(fm)
    local roleId = to_number(robot.get("roleId"))
    local playerId = to_number(fm.playerId)
    if roleId ~= nil and playerId == roleId then
        clearGuildState(roleId)
    end
end

-- 21:14 当前玩家成员数据更新（member.guildData.playerId==自己才处理）
local function handleMemberUpdate(fm)
    if type(fm.member) ~= "table" or type(fm.member.guildData) ~= "table" then return end
    local roleId = to_number(robot.get("roleId"))
    local guildData = fm.member.guildData
    local playerId = to_number(guildData.playerId)
    if roleId == nil or playerId ~= roleId then return end

    local guildId = to_number(fm.uid) or to_number(guildData.guildUid)
    if guildId == nil or guildId == 0 then
        robot.set_path("playerData.guildInfo.guildId", 0)
        robot.set_path("playerData.guildInfo.mydata.guildUid", 0)
        robot.set("guildMembers", nil)
        robot.set("guildApplyList", nil)
        log.info("guild_drain: 成员数据清空更新 roleId=" .. tostring(roleId))
        return
    end

    robot.set_path("playerData.guildInfo.guildId", guildId)
    robot.set_path("playerData.guildInfo.mydata", guildData)
    if fm.guildName ~= nil then
        robot.set_path("playerData.guildInfo.baseInfo.name", fm.guildName)
    end
end

-- 21:13 社团信息更新（多字段双写 currentGuildInfo + playerData.guildInfo）
local function handleUpdate(fm)
    if fm.baseInfo ~= nil then
        robot.set_path("currentGuildInfo.baseInfo", fm.baseInfo)
        robot.set_path("playerData.guildInfo.baseInfo", fm.baseInfo)
        if fm.baseInfo.uid ~= nil then
            robot.set_path("playerData.guildInfo.guildId", fm.baseInfo.uid)
        end
    end
    if fm.baseSetting ~= nil then
        robot.set_path("currentGuildInfo.baseSetting", fm.baseSetting)
        robot.set_path("playerData.guildInfo.baseSetting", fm.baseSetting)
    end
    if fm.statistics ~= nil then
        robot.set_path("currentGuildInfo.statistics", fm.statistics)
    end
    if fm.timeRecord ~= nil then
        robot.set_path("currentGuildInfo.timeRecord", fm.timeRecord)
    end
    if fm.routData ~= nil then
        robot.set_path("currentGuildInfo.route", fm.routData)
        robot.set_path("playerData.guildInfo.routeData", fm.routData)
    end
end

function execute(r)
    drainRoute({cmd = 21, act = 15}, "Game.GuildNotifyNewMemberS2C", handleJoin)
    drainRoute({cmd = 21, act = 16}, "Game.GuildNotifyKickMemberS2C", handleKick)
    drainRoute({cmd = 21, act = 14}, "Game.GuildMemberUpdateNotifyS2C", handleMemberUpdate)
    drainRoute({cmd = 21, act = 13}, "Game.GuildUpdateNotifyS2C", handleUpdate)
    return nil
end
