-- ranked_member_enqueue.lua: 队员生成 token、写 wait 标记、入待命队列
-- 失败（Redis 不可用）返回 err 触发 skip，下轮重来——不降级单排。
local share = require("share")
local robot = require("robot")
local utils = require("utils")
local log = require("log")

local PREFIX = "ranked:v2"
local QUEUE_TTL = 120
local WAIT_MARK_TTL = 30

local function waitMarkKey(id) return PREFIX .. ":wait:" .. tostring(id) end

function execute(r)
    local roleId = tonumber(robot.get("roleId"))
    local account = robot.get("account") or ""
    if not roleId then
        return robot.error(54, "RankedMemberEnqueue 缺少 roleId")
    end
    local token = tostring(roleId) .. ":" .. tostring(utils.time_ms())
    robot.set("rankedQueueToken", token)
    robot.delete("rankedRoundFailed")

    share.hash_set(waitMarkKey(roleId), "token", token, WAIT_MARK_TTL)
    share.hash_set(waitMarkKey(roleId), "status", "waiting", WAIT_MARK_TTL)
    share.hash_set(waitMarkKey(roleId), "updatedAtMs", tostring(utils.time_ms()), WAIT_MARK_TTL)

    local entry = tostring(roleId) .. "|" .. account .. "|" .. token .. "|" .. tostring(utils.time_ms())
    local ok, err = share.queue_push(PREFIX .. ":waiting", entry, QUEUE_TTL)
    if err or not ok then
        share.hash_set(waitMarkKey(roleId), "status", "queue_failed", WAIT_MARK_TTL)
        log.warn("排位队员入队列失败，跳过本轮: err=" .. tostring(err))
        return robot.error(54, "RankedMemberEnqueue 入队失败")
    end
    log.info("排位队员已入待命队列: roleId=" .. tostring(roleId))
    return nil
end
