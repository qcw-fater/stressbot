-- sdc_can_enter.lua: 搜打撤进战斗门禁（boolean lua 条件），兼诊断
-- 记录战斗四件套的实际值/类型，返回是否齐备。listen_start_loading 成功(已 log 开始加载)后这些应全部就绪。
local robot = require("robot")
local log = require("log")

function execute(r)
    local battleId = robot.get("battleId")
    local battleSession = robot.get("battleSession")
    local battleAddress = robot.get("battleAddress")
    local battleSecretKey = robot.get("battleSecretKey")

    local idOk = battleId ~= nil and battleId ~= 0 and battleId ~= "0"
    local sessOk = battleSession ~= nil and battleSession ~= 0 and battleSession ~= "0"
    local addrOk = battleAddress ~= nil and battleAddress ~= ""
    local keyOk = battleSecretKey ~= nil and battleSecretKey ~= ""
    local ok = idOk and sessOk and addrOk and keyOk

    local detail = "SDC 进战门禁: battleId=" .. tostring(battleId) .. "(" .. type(battleId) .. ")"
        .. " battleSession=" .. tostring(battleSession) .. "(" .. type(battleSession) .. ")"
        .. " battleAddress=" .. tostring(battleAddress)
        .. " secretKey_type=" .. type(battleSecretKey)
        .. " idOk=" .. tostring(idOk) .. " sessOk=" .. tostring(sessOk)
        .. " addrOk=" .. tostring(addrOk) .. " keyOk=" .. tostring(keyOk)
        .. " -> canEnter=" .. tostring(ok)
    if ok then
        log.info(detail)
    else
        log.warn(detail)
    end
    return ok
end
