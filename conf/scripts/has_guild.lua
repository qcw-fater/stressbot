-- has_guild.lua: 判断当前机器人是否已加入社团
-- guildId 是 int64：声明式响应 store 可能保存整数，Lua 推送为避免精度损失会保存字符串，
-- 因此这里只做字符串化的零值判断，不能 tonumber。
local robot = require("robot")

function execute(r)
    local guildId = robot.get_path("playerData.guildInfo.guildId")
    return guildId ~= nil and tostring(guildId) ~= "" and tostring(guildId) ~= "0"
end
