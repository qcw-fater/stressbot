-- match_succeed.lua: 等待匹配成功（tcp_listen CMD=3, ACT=1）
-- 从 MatchSucceedS2C 的 actorList 中提取自己的 battleSession（校验ID）
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    -- 轮询监听匹配成功消息，超时 600 秒（10 分钟），轮询 1 秒
    local resp, recv = network.tcp_listen("logic", {cmd=3, act=1}, "Game.MatchSucceedS2C", 600, 1000)
    if not resp then
        log.error("MatchSucceed 超时")
        return 31, 0, recv  -- 31=LISTEN_TIMEOUT
    end

    local ok, err = pcall(function()
        -- 从 actorList 中找到自己的 PlayerGameInfo，提取 matchData.sessionId（校验ID）和 index
        local myPlayerId = tonumber(robot.get("roleId"))
        local actorCount = proto.list_size(resp, "actorList")
        if actorCount > 0 and myPlayerId then
            for i = 1, actorCount do
                local actor = proto.list_get(resp, "actorList", i)
                local pid = tonumber(proto.get_path(actor, "playerId"))
                if pid and pid == myPlayerId then
                    local sessionId = proto.get_path(actor, "matchData.sessionId")
                    if sessionId then
                        robot.set("battleSession", sessionId)
                    end
                    break
                end
            end
        end

        local battleArea = proto.get_path(resp, "battleArea")
        if battleArea then
            robot.set("battleArea", battleArea)
        end
    end)

    if ok then
        log.info("匹配成功: battleSession=" .. tostring(robot.get("battleSession"))
            .. " battleArea=" .. tostring(robot.get("battleArea")))
    else
        log.error("MatchSucceed 解析失败: " .. tostring(err))
        return 54, 0, recv  -- 54=LUA_EXIT_CODE
    end

    return 0, 0, recv
end
