-- match_succeed.lua: 等待匹配成功（waitListen CMD=3, ACT=1）
-- 从 MatchSucceedS2C 的 actorList 中提取自己的 battleSession（校验ID）
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    -- 轮询监听匹配成功消息，超时 600 秒（10 分钟）
    local resp, recv = network.wait_listen("logic", {cmd=3, act=1}, "Game.MatchSucceedS2C", 600)
    if not resp then
        log.error("MatchSucceed 超时")
        return 1, 0, recv
    end

    local ok, err = pcall(function()
        local fieldMap = proto.get_field_map(resp)

        -- 从 actorList 中找到自己的 PlayerGameInfo，提取 matchData.sessionId（校验ID）和 index
        local myPlayerId = tonumber(robot.get("roleId"))
        local actorList = fieldMap.actorList
        if actorList and myPlayerId then
            for _, actor in ipairs(actorList) do
                local pid = tonumber(actor.playerId)
                if pid and pid == myPlayerId then
                    -- 提取 matchData 中的 sessionId（校验ID）和 index
                    if actor.matchData then
                        if actor.matchData.sessionId then
                            robot.set("battleSession", actor.matchData.sessionId)
                        end
                    end
                    break
                end
            end
        end

        -- 存储 battleArea
        if fieldMap.battleArea then
            robot.set("battleArea", fieldMap.battleArea)
        end
    end)

    if ok then
        log.info("匹配成功: battleSession=" .. tostring(robot.get("battleSession"))
            .. " battleArea=" .. tostring(robot.get("battleArea")))
    else
        log.error("MatchSucceed 解析失败: " .. tostring(err))
        return 1, 0, recv
    end

    return 0, 0, recv
end
