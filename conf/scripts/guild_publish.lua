-- guild_publish.lua: 发布或刷新本任务固定创建者的在线社团记录
-- 前置契约：state.index 在任务内全局唯一、从 0 递增；每 15 个机器人由 index%15==0 的机器人创建社团。
local share = require("share")
local robot = require("robot")
local utils = require("utils")
local log = require("log")

local POOL_KEY = "guild:v3:pool"
local POOL_TTL = 120
local GROUP_SIZE = 15

function execute(r)
    local index = tonumber(robot.get("index"))
    if index == nil or index < 0 or index % 1 ~= 0 then
        return robot.error(54, "GuildPublish index 必须是非负整数")
    end
    if index % GROUP_SIZE ~= 0 then
        return nil
    end

    local guildId = robot.get_path("playerData.guildInfo.guildId")
    local createdGuildId = robot.get("taskGuildCreatedId")
    local position = tonumber(robot.get_path("playerData.guildInfo.mydata.position"))
    local name = robot.get_path("playerData.guildInfo.baseInfo.name")
    if guildId == nil or tostring(guildId) == "" or tostring(guildId) == "0"
        or createdGuildId == nil or tostring(createdGuildId) ~= tostring(guildId)
        or position ~= 0 or type(name) ~= "string" or name == "" then
        return nil
    end

    local ok, err = share.hash_set(POOL_KEY, tostring(index), {
        guildId = tostring(guildId),
        name = name,
        leaderIndex = index,
        leaderRoleId = tonumber(robot.get("roleId")) or 0,
        updatedAtMs = utils.time_ms(),
    }, POOL_TTL)
    if err or not ok then
        log.warn("发布本任务社团失败: index=" .. tostring(index) .. " err=" .. tostring(err))
        return err or robot.error(54, "GuildPublish 写入社团池失败")
    end
    return nil
end
