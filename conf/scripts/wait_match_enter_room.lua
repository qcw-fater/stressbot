-- wait_match_enter_room.lua: 等待当前轮房间成功(3:5)或服务器明确退回大厅(2:10)。
-- 准备失败时服务器会先发 MainBackLobbyS2C；若只等待 3:5，一个未确认玩家会让全房间其余玩家空等到超时。
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")
local log = require("log")

local WAIT_TIMEOUT_MS = 60 * 1000
local POLL_MS = 100
local EMPTY_QUEUE_CODE = 31

local function valid_id(value)
    return value ~= nil and value ~= 0 and value ~= "0"
end

local function try_parse(route, protoName)
    local err, raw = network.try_tcp_listen("logic", route)
    if err then
        if tonumber(err.code) == EMPTY_QUEUE_CODE then
            return nil, nil
        end
        return err, nil
    end
    if raw == nil or raw == "" then
        return nil, nil
    end

    local ok, msg = pcall(proto.parse, protoName, raw)
    if not ok then
        return robot.error(12, protoName .. " 解析失败: " .. tostring(msg)), nil
    end
    return nil, msg
end

local function describe_player_ids(msg)
    local values = {}
    local count = proto.list_size(msg, "playerIdList")
    for i = 1, count do
        values[#values + 1] = tostring(proto.list_get(msg, "playerIdList", i))
    end
    return table.concat(values, ",")
end

function execute(r)
    local roleId = robot.get("roleId") or robot.get("playerId")
    local deadlineMs = utils.time_ms() + WAIT_TIMEOUT_MS

    while utils.time_ms() < deadlineMs do
        -- 先处理负信号：Confirm 后出现的返回大厅消息属于当前轮，必须立即结束依赖链。
        local backErr, back = try_parse({cmd=2, act=10}, "Game.MainBackLobbyS2C")
        if backErr then
            return backErr
        end
        if back ~= nil then
            local reason = tonumber(proto.get_field(back, "Reason"))
                or tonumber(proto.get_field(back, "reason")) or 0
            local notReadyEndTime = proto.get_field(back, "notReadyEndTime")
            local playerIds = describe_player_ids(back)
            log.warn("当前轮匹配已返回大厅，立即结束本轮: roleId=" .. tostring(roleId)
                .. " reason=" .. tostring(reason)
                .. " playerIds=" .. playerIds
                .. " notReadyEndTime=" .. tostring(notReadyEndTime))
            return robot.error(54, "当前轮匹配已返回大厅: reason=" .. tostring(reason)
                .. " roleId=" .. tostring(roleId))
        end

        local enterErr, enter = try_parse({cmd=3, act=5}, "Game.MatchEnterRoomS2C")
        if enterErr then
            return enterErr
        end
        if enter ~= nil then
            local candidate = {
                roomId = proto.get_field(enter, "roomId"),
                overTime = tonumber(proto.get_field(enter, "overTime")),
            }
            local nowSec = math.floor(utils.time_ms() / 1000)
            if not valid_id(candidate.roomId) then
                return robot.error(54, "当前轮 MatchEnterRoom 缺少 roomId: roleId="
                    .. tostring(roleId))
            end
            if candidate.overTime == nil then
                return robot.error(54, "当前轮 MatchEnterRoom 缺少 overTime: roleId="
                    .. tostring(roleId) .. " roomId=" .. tostring(candidate.roomId))
            end
            if candidate.overTime < nowSec then
                log.warn("收到已过期 MatchEnterRoom，结束本轮: roleId=" .. tostring(roleId)
                    .. " roomId=" .. tostring(candidate.roomId)
                    .. " overTime=" .. tostring(candidate.overTime)
                    .. " now=" .. tostring(nowSec))
                return robot.error(54, "当前轮 MatchEnterRoom 已过期: roleId="
                    .. tostring(roleId) .. " roomId=" .. tostring(candidate.roomId))
            end

            robot.set("matchRoomId", candidate.roomId)
            log.info("已进入当前轮房间，准备 BP: roleId=" .. tostring(roleId)
                .. " roomId=" .. tostring(candidate.roomId)
                .. " overTime=" .. tostring(candidate.overTime))
            return nil
        end

        -- 2:10 与状态推送均由服务端返回大厅路径产生；状态只作丢包兜底，不替代明确消息。
        local playerStatus = tonumber(robot.get("playerStatus"))
        if playerStatus == 2 or playerStatus == 3 then
            log.warn("等待进入房间时已返回大厅/队伍，结束本轮: roleId="
                .. tostring(roleId) .. " playerStatus=" .. tostring(playerStatus))
            return robot.error(54, "等待进入房间时玩家状态已回退: roleId="
                .. tostring(roleId) .. " playerStatus=" .. tostring(playerStatus))
        end

        utils.sleep(POLL_MS)
    end

    return robot.error(31, "等待当前轮房间终态超时: roleId=" .. tostring(roleId)
        .. " playerStatus=" .. tostring(robot.get("playerStatus")))
end
