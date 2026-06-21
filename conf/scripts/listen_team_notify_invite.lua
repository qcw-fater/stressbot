-- listen_team_notify_invite.lua: 处理 TeamNotifyInviteS2C (route 5:6)
-- 仅处理排位邀请 (model=2)，将邀请信息缓存到本地 state 供 ranked_team_accept 使用
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function onMessage(r, msg)
    if not msg then return end

    local ok, err = pcall(function()
        -- 用 get_field_map 一次性拿到所有字段（子消息自动为 table）
        local fm = proto.get_field_map(msg)
        if not fm then return end

        local model = tonumber(fm.model)
        -- 只处理排位邀请，其他模式忽略
        if model ~= 2 then
            log.info("排位邀请回调: 非 排位模式 model=" .. tostring(fm.model))
            return
        end

        -- 邀请人信息在 inviteInfo 子消息中（已为 Lua table）
        local inviterId = nil
        if fm.inviteInfo and type(fm.inviteInfo) == "table" then
            inviterId = fm.inviteInfo.playerId
        end

        local inviteData = {
            teamId       = fm.teamId,
            model        = fm.model,
            gType        = fm.gType,
            battleModeId = fm.battleModeId,
            roomId       = fm.roomId,
            isInvite     = fm.isInvite,
            inviterId    = inviterId,
            beInvite     = fm.beInvite,
        }

        robot.set("rankedTeamInvite", inviteData)
        robot.set("rankedTeamInviteReceived", true)

        log.info("收到排位组队邀请: inviter=" .. tostring(inviterId)
            .. " teamId=" .. tostring(fm.teamId)
            .. " model=" .. tostring(fm.model))
    end)
    if not ok then
        log.warn("排位回调 teamNotifyInvite 解析失败: " .. tostring(err))
    end
end
