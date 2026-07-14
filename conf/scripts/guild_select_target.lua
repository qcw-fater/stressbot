-- guild_select_target.lua: 从本任务在线社团池随机选择搜索/申请目标
-- 只过滤无效或明显离线记录；重复申请、容量与入社条件交给服务端返回真实业务结果。
local share = require("share")
local robot = require("robot")
local utils = require("utils")
local log = require("log")

local POOL_KEY = "guild:v3:pool"
local ONLINE_MS = 60000

function execute(r)
    robot.delete("taskGuildTargetId")
    robot.delete("taskGuildTargetName")
    robot.set("taskGuildTargetReady", false)

    local pool, err = share.hash_get_all(POOL_KEY)
    if err then
        log.warn("读取本任务社团池失败: err=" .. tostring(err))
        return err
    end
    if type(pool) ~= "table" then
        return nil
    end

    local now = utils.time_ms()
    local candidates = {}
    for _, item in pairs(pool) do
        if type(item) == "table" then
            local guildId = item.guildId
            local name = item.name
            local updatedAtMs = tonumber(item.updatedAtMs)
            if guildId ~= nil and tostring(guildId) ~= "" and tostring(guildId) ~= "0"
                and type(name) == "string" and name ~= ""
                and updatedAtMs ~= nil and now - updatedAtMs <= ONLINE_MS then
                candidates[#candidates + 1] = item
            end
        end
    end

    local target = utils.random_pick(candidates)
    if target == nil then
        return nil
    end

    robot.set("taskGuildTargetId", tostring(target.guildId))
    robot.set("taskGuildTargetName", target.name)
    robot.set("taskGuildTargetReady", true)
    return nil
end
