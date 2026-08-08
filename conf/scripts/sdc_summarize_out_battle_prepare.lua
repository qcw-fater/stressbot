-- 只基于声明式 45:1 响应计算局外背包摘要与本局随机出售目标。
local robot = require("robot")
local utils = require("utils")
local log = require("log")

local function valid_guid(value)
    return value ~= nil and tostring(value) ~= "" and tostring(value) ~= "0"
end

local function guid_set(values)
    local result = {}
    for _, value in ipairs(type(values) == "table" and values or {}) do
        if valid_guid(value) then result[tostring(value)] = true end
    end
    return result
end

function execute(r)
    local code = tonumber(robot.get("sdcOutBattlePrepareErrorCode")) or 0
    if code ~= 0 then return robot.error(code, "回大厅刷新战备返回业务错误") end
    local data = robot.get("sdcOutBattlePrepareSnapshot")
    if type(data) ~= "table" then return robot.error(54, "回大厅刷新战备缺少 data") end

    local initial, current, newItems = guid_set(robot.get("sdcInitialItemGuids")), {}, {}
    local maxSlots, usedSlots = 0, 0
    for _, pack in ipairs(data.packSlots or {}) do
        maxSlots = maxSlots + math.max(tonumber(pack.maxSlots) or 0, 0)
        usedSlots = usedSlots + #(pack.items or {})
        for _, item in ipairs(pack.items or {}) do
            if valid_guid(item.guid) then
                current[#current + 1] = item.guid
                if not initial[tostring(item.guid)] then newItems[#newItems + 1] = item.guid end
            end
        end
    end
    -- 背包里有几件就最多卖几件，上限 2 件（避免久跑后背包只进不出被填满）。
    local sellCount = math.min(#current, 2)
    local sellGuids = sellCount > 0 and utils.random_pick_n(current, sellCount) or {}
    robot.set("sdcOutBattlePackMaxSlots", maxSlots)
    robot.set("sdcOutBattlePackUsedSlots", usedSlots)
    robot.set("sdcOutBattlePackFreeSlots", math.max(maxSlots - usedSlots, 0))
    robot.set("sdcOutBattleNewItemGuids", newItems)
    robot.set("sdcOutBattleRandomSellGuid", sellGuids)
    robot.set("sdcOutBattleSellCount", #sellGuids)
    log.info("搜打撤回大厅战备摘要完成: current=" .. tostring(#current)
        .. " new=" .. tostring(#newItems) .. " sellCount=" .. tostring(#sellGuids))
    return nil
end
