-- register_battle.lua: 注册到战斗服（BattleRegisterC2S CMD=4, ACT=1）
-- 需要先从 listen_start_loading 存储的 fighterIndex, battleId, battleSession
-- 战斗服可能还没准备好，加入重试机制；多次重试产生的 WireBytes 由 network API 自动累计。
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")
local log = require("log")

function execute(r)
    local fighterIndex = robot.get("fighterIndex")
    local battleId = robot.get("battleId") or 0
    local battleSession = robot.get("battleSession") or 0

    if not fighterIndex then
        log.error("RegisterBattle 缺少 fighterIndex: battleId=" .. tostring(battleId)
            .. " battleSession=" .. tostring(battleSession))
        return robot.error(54, "RegisterBattle 缺少 fighterIndex: battleId=" .. tostring(battleId)
            .. " battleSession=" .. tostring(battleSession))  -- 54=LUA_SCRIPT_CHECK：业务前置条件不足
    end

    local lastErr = nil  -- 兜底；首轮失败前为 nil

    -- 战斗服可能还未创建好房间，重试最多 5 次，每次间隔 2 秒
    local maxRetry = 5
    for attempt = 1, maxRetry do
        local msg = proto.create("Game.BattleRegisterC2S")
        proto.set_field(msg, "index", fighterIndex)
        proto.set_field(msg, "battleId", battleId)
        proto.set_field(msg, "sessionId", battleSession)

        local err = network.tcp_request("battle", {cmd=4, act=1}, msg, "Game.BattleRegisterS2C")
        lastErr = err
        if not err then
            log.info("RegisterBattle 成功: index=" .. tostring(fighterIndex)
                .. " battleId=" .. tostring(battleId)
                .. " battleSession=" .. tostring(battleSession))
            return nil
        end

        log.warn("RegisterBattle 第 " .. tostring(attempt) .. "/" .. tostring(maxRetry) .. " 次尝试失败: code="
            .. tostring(err.code) .. " detail=" .. tostring(err.detail)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " battleSession=" .. tostring(battleSession))
        if attempt < maxRetry then
            log.info("RegisterBattle 等待 2 秒后重试: nextAttempt=" .. tostring(attempt + 1)
                .. " maxRetry=" .. tostring(maxRetry))
            utils.sleep(2000)
        end
    end

    local codeText = lastErr and tostring(lastErr.code) or "nil"
    local detailText = lastErr and lastErr.detail or ""
    log.error("RegisterBattle 最终失败: retry=" .. tostring(maxRetry)
        .. " lastCode=" .. codeText
        .. " detail=" .. detailText
        .. " battleId=" .. tostring(battleId)
        .. " fighterIndex=" .. tostring(fighterIndex)
        .. " battleSession=" .. tostring(battleSession))
    return lastErr  -- 透传最后一次失败的 err table
end
