local network = require("network")
local proto = require("proto")
local log = require("log")

function execute(r)
    local ids = {4, 5, 7, 9}
    local allSend, allRecv = 0, 0
    local failedCount = 0
    local lastCode = 0

    for _, id in ipairs(ids) do
        local msg = proto.create("Game.MainGetLevelRewardC2S")
        proto.set_field(msg, "Id", id)

        local code, data, sent, recv = network.tcp_request("logic", {cmd=2, act=26}, msg, "Game.MainGetLevelRewardS2C")

        allSend = allSend + (sent or 0)
        allRecv = allRecv + (recv or 0)

        if code ~= 0 or data == nil then
            failedCount = failedCount + 1
            if code ~= 0 then
                lastCode = code
            else
                lastCode = 54
            end
            log.error("解锁基础功能失败: service=logic route=2:26 id=" .. tostring(id)
                .. " code=" .. tostring(code)
                .. " hasData=" .. tostring(data ~= nil)
                .. " sent=" .. tostring(sent)
                .. " recv=" .. tostring(recv))
        else
            log.debug("解锁基础功能成功: id=" .. tostring(id)
                .. " sent=" .. tostring(sent)
                .. " recv=" .. tostring(recv))
        end
    end

    if failedCount > 0 then
        log.warn("解锁基础功能存在失败: failed=" .. tostring(failedCount)
            .. " total=" .. tostring(#ids)
            .. " lastCode=" .. tostring(lastCode)
            .. " totalSend=" .. tostring(allSend)
            .. " totalRecv=" .. tostring(allRecv))
        return lastCode, allSend, allRecv
    end

    return 0, allSend, allRecv
end
