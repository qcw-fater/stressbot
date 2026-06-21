-- listen_team_update_info.lua: 处理 TeamUpdateInfoS2C (route 5:3)
-- 存储队伍信息，统计成员数供队长判断队伍是否满员
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function onMessage(r, msg)
    if not msg then return end

    -- 安全解析: msg 可能因 proto 解析失败而不是 proto userdata
    -- 整个函数体用 pcall 包裹，任何 proto 解析错误都不影响主流程
    local ok, err = pcall(function()
        local fieldMap = proto.get_field_map(msg)
        if not fieldMap then return end

        robot.set("teamData", fieldMap)

        -- teamData 是 teamRoomInfo 子消息，直接在 fieldMap 中
        local teamData = fieldMap.teamData
        if teamData then
            -- 保存 teamId（teamRoomInfo.id）到本地 state
            if teamData.id then
                robot.set("teamId", tonumber(teamData.id))
            end
            -- 统计 Members 列表长度
            local members = teamData.Members
            if members then
                local count = 0
                for _ in pairs(members) do count = count + 1 end
                robot.set("teamMemberCount", count)
            end
            -- 提取队长 ID
            if teamData.headerId then
                robot.set("teamHeaderId", teamData.headerId)
            end
        end
    end)
    if not ok then
        log.debug("排位回调 teamUpdateInfo 解析失败（可忽略）: " .. tostring(err))
    end
end
