-- has_guild.lua: 按旧工具 IsInGuild 语义判断当前是否在社团
-- 前提：JoinGuild 申请待审核时不能写 playerData.guildInfo.guildId；
-- 只有 CreateGuild、直接加入成功、入会推送、成员更新才维护该字段。
local robot = require("robot")

local function to_number(v)
    if v == nil then return nil end
    if type(v) == "number" then return v end
    return tonumber(v)
end

function execute(r)
    local guildId = to_number(robot.get_path("playerData.guildInfo.guildId"))
    return guildId ~= nil and guildId ~= 0
end
