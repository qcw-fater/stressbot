-- register_battle.lua: 注册到战斗服（BattleRegisterC2S CMD=4, ACT=1）
-- 需要先从 listen_start_loading 存储的 fighterIndex, battleId, battleSession
-- 战斗服可能还没准备好，加入重试机制；多次重试的字节都累加到 _send/_recv。
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
        return 54, 0, 0  -- 54=LUA_EXIT_CODE：业务前置条件不足
    end

    local _send, _recv = 0, 0
    local lastCode = 54  -- 兜底；首轮失败前不该被使用

    -- 战斗服可能还未创建好房间，重试最多 5 次，每次间隔 2 秒
    local maxRetry = 5
    for attempt = 1, maxRetry do
        local msg = proto.create("Game.BattleRegisterC2S")
        proto.set_field(msg, "index", fighterIndex)
        proto.set_field(msg, "battleId", battleId)
        proto.set_field(msg, "sessionId", battleSession)

        local code, _, sent, recv = network.tcp_request("battle", {cmd=4, act=1}, msg, "Game.BattleRegisterS2C")
        _send = _send + sent
        _recv = _recv + recv
        lastCode = code
        if code == 0 then
            log.info("RegisterBattle 成功: index=" .. tostring(fighterIndex)
                .. " battleId=" .. tostring(battleId)
                .. " battleSession=" .. tostring(battleSession)
                .. " send=" .. tostring(_send)
                .. " recv=" .. tostring(_recv))
            return 0, _send, _recv
        end

        log.warn("RegisterBattle 第 " .. tostring(attempt) .. "/" .. tostring(maxRetry) .. " 次尝试失败: code="
            .. tostring(code)
            .. " sent=" .. tostring(sent)
            .. " recv=" .. tostring(recv)
            .. " battleId=" .. tostring(battleId)
            .. " fighterIndex=" .. tostring(fighterIndex)
            .. " battleSession=" .. tostring(battleSession))
        if attempt < maxRetry then
            log.info("RegisterBattle 等待 2 秒后重试: nextAttempt=" .. tostring(attempt + 1)
                .. " maxRetry=" .. tostring(maxRetry))
            utils.sleep(2000)
        end
    end

    log.error("RegisterBattle 最终失败: retry=" .. tostring(maxRetry)
        .. " lastCode=" .. tostring(lastCode)
        .. " totalSend=" .. tostring(_send)
        .. " totalRecv=" .. tostring(_recv)
        .. " battleId=" .. tostring(battleId)
        .. " fighterIndex=" .. tostring(fighterIndex)
        .. " battleSession=" .. tostring(battleSession))
    return lastCode, _send, _recv  -- 透传最后一次失败的真实 code
end
