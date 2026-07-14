-- is_guild_manager.lua: 判断当前机器人是否具备社团管理权限（会长或小队长）
local robot = require("robot")
local log = require("log")

local function to_number(v)
    if v == nil then return nil end
    if type(v) == "number" then return v end
    local n = tonumber(v)
    return n
end

function execute(r)
    local roleId = to_number(robot.get("roleId"))
    local position = to_number(robot.get_path("playerData.guildInfo.mydata.position"))
    local guildId = robot.get_path("playerData.guildInfo.guildId")

    local isLeader = position ~= nil and position == 0
    local isLed = position ~= nil and position == 1
    local isManager = isLeader or isLed

    log.debug("判断社团管理权限: roleId=" .. tostring(roleId)
        .. " guildId=" .. tostring(guildId)
        .. " position=" .. tostring(position)
        .. " result=" .. tostring(isManager))
    return isManager
end
