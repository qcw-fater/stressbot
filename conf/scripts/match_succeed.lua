-- match_succeed.lua: 等待匹配成功（tcp_listen CMD=3, ACT=1）
-- 从 MatchSucceedS2C 的 actorList 中提取自己的 battleSession（校验ID）
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    local roleId = robot.get("roleId")

    -- 轮询监听匹配成功消息，超时 1860 秒（服务器匹配超时 1800 秒 + 60 秒调度余量），轮询 1 秒
    local err, resp = network.tcp_listen("logic", {cmd=3, act=1}, "Game.MatchSucceedS2C", 1860, 1000)
    if err then
        log.error("匹配成功消息等待失败: service=logic route=3:1 proto=Game.MatchSucceedS2C timeoutSec=1860 pollMs=1000 roleId="
            .. tostring(roleId) .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    local actorCount = 0
    local ok, err = pcall(function()
        -- 从 actorList 中找到自己的 PlayerGameInfo，提取 matchData.sessionId（校验ID）和 index
        local myPlayerId = tonumber(roleId)
        actorCount = proto.list_size(resp, "actorList")
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

    if not ok then
        log.error("MatchSucceed 解析失败: roleId=" .. tostring(roleId)
            .. " actorCount=" .. tostring(actorCount)
            .. " err=" .. tostring(err))
        return robot.error(12, "MatchSucceed 解析失败: roleId=" .. tostring(roleId)
            .. " actorCount=" .. tostring(actorCount)
            .. " err=" .. tostring(err))  -- 12=PARSE_FAILED：协议层异常
    end

    local battleSession = robot.get("battleSession")
    local battleArea = robot.get("battleArea")
    if not battleSession then
        log.error("匹配成功但未找到自己的 battleSession: roleId=" .. tostring(roleId)
            .. " actorCount=" .. tostring(actorCount)
            .. " battleArea=" .. tostring(battleArea))
        return robot.error(54, "MatchSucceed battleSession 缺失: roleId=" .. tostring(roleId)
            .. " actorCount=" .. tostring(actorCount)
            .. " battleArea=" .. tostring(battleArea))  -- 54=LUA_EXIT_CODE：脚本断言失败
    end

    log.info("匹配成功: roleId=" .. tostring(roleId)
        .. " actorCount=" .. tostring(actorCount)
        .. " battleSession=" .. tostring(battleSession)
        .. " battleArea=" .. tostring(battleArea))

    return nil
end
