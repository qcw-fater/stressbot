-- listen_team_join.lua: 处理 TeamJoinS2C (route 5:10)
-- 存储入队结果，code==0 表示成功
local robot = require("robot")
local proto = require("proto")
local log = require("log")
local share = require("share")
local utils = require("utils")

local TEAM_PREFIX = "ranked:v2:team:"
local TEAM_TTL = 600

-- 这些 helper 处理 proto 回调里取出的字段值。失效/为空的 proto Go userdata，
-- 其 __eq / String 路径会触发 Go 侧 nil pointer，且该 panic 无法被 Lua pcall 捕获，
-- 会冒泡成 framework/53 动作失败。所以一律先用 type(v)（只读类型标签，不解引用 Go
-- 指针）判别：绝不直接 `v == nil` 比较、也绝不对 userdata 调用 tostring。
local function safeString(v)
    local t = type(v)
    if t == "nil" then return "" end
    if t == "string" then return v end
    if t == "number" or t == "boolean" then return tostring(v) end
    return "<" .. t .. ">"
end

local function safeNumber(v)
    local t = type(v)
    if t == "number" then return v end
    if t == "string" then return tonumber(v) end
    return nil
end

local function safeValue(v)
    local n = safeNumber(v)
    if n ~= nil then return n end
    if type(v) == "string" and v ~= "" then return v end
    if type(v) == "boolean" then return v end
    return nil
end

-- 取字段后立刻归一成纯 Lua 标量，避免 proto userdata 逸出到后续拼接 / 比较 / hash 写入。
local function safeGetField(msg, field)
    local ok, value = pcall(function()
        local v = proto.get_field(msg, field)
        local t = type(v)
        if t == "number" or t == "string" or t == "boolean" then return v end
        return nil
    end)
    if not ok then
        log.debug("排位回调 teamJoin 字段读取失败（可忽略）: field=" .. tostring(field))
        return nil
    end
    return value
end

local function setStateValue(key, raw)
    local value = safeValue(raw)
    if value ~= nil then
        robot.set(key, value)
    else
        robot.delete(key)
    end
    return value
end

function onMessage(r, msg)
    if not msg then return end

    local ok, err = pcall(function()
        local rawCode   = safeGetField(msg, "code")
        local rawTeamId = safeGetField(msg, "teamId")
        local rawModel  = safeGetField(msg, "model")
        local rawGType  = safeGetField(msg, "gType")
        local rawModeId = safeGetField(msg, "modeId")
        local rawTsId   = safeGetField(msg, "tsId")

        local code   = setStateValue("teamJoinCode", rawCode)
        local teamId = setStateValue("teamId", rawTeamId)
        local model  = setStateValue("teamModel", rawModel)
        setStateValue("teamGType", rawGType)
        setStateValue("teamModeId", rawModeId)
        setStateValue("teamTsId", rawTsId)

        local codeNum = safeNumber(code) or -1
        if codeNum == 0 then
            robot.set("rankedTeamAcceptDone", true)

            local roleIdText = safeString(robot.get("roleId"))
            local teamKeyText = safeString(robot.get("rankedTeamKey"))
            if roleIdText ~= "" and teamKeyText ~= "" then
                local memberKey = TEAM_PREFIX .. teamKeyText .. ":members"
                local _, setErr = share.hash_set(memberKey, roleIdText, {
                    playerId = safeNumber(roleIdText) or roleIdText,
                    account = safeString(robot.get("account")),
                    role = safeString(robot.get("rankedTeamRole")) ~= "" and safeString(robot.get("rankedTeamRole")) or "member",
                    status = "joined",
                    teamId = teamId,
                    joinedAtMs = safeString(utils.time_ms()),
                }, TEAM_TTL)
                if setErr then
                    log.debug("排位回调 teamJoin 写入成员状态失败（可忽略）: err=" .. safeString(setErr))
                end
            end

            log.info("加入排位队伍成功: teamId=" .. safeString(teamId)
                .. " model=" .. safeString(model))
        else
            log.warn("加入排位队伍失败: code=" .. safeString(code)
                .. " teamId=" .. safeString(teamId))
        end
    end)
    if not ok then
        log.warn("排位回调 teamJoin 解析失败: " .. safeString(err))
    end
end
