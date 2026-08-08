-- ranked_leave_team.lua: 强制退队（每轮开头 + 结尾各调用一次）
-- 核心修复"人数越跑越少"：服务端 TeamLeave 强校验 teamId（错/空返回 308，无接口盲退），
-- 一旦本地丢失 teamId 就永久卡队。故本脚本铁律——**退队未确认成功绝不清 teamId**：
--   - 成功（code 0）→ 清 teamId/teamData
--   - 288/311 匹配状态未清 → 队长/单排先取消匹配再退一次；仍失败则保留 teamId 下轮重试
--   - 308 teamId 失配/不在队 → 保留 teamId（TeamCreate 成功会覆盖；真卡队则下轮继续）
--   - 其它（网络等）→ 保留 teamId 下轮重试
-- 永不返回 err、永不 skip（清理节点必须总执行）。
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

-- 注意：teamId 是 int64 雪花 ID（>2^53），Lua number(float64) 无法精确表示。
-- robot.get 返回的是字符串（goValueToLua 对超大 int64 用 LString 保精度），
-- 必须原样透传给 proto.set_field（底层 strconv.ParseInt 精确还原），绝不能 tonumber——
-- 否则精度丢失导致 TeamLeave 308 失配、退队失败、下一轮 TeamCreate 314 卡死（人数越跑越少）。
local function currentTeamId()
    local tid = robot.get("teamId")
    if tid and tid ~= 0 and tid ~= "0" then return tid end
    local td = robot.get("teamData")
    if type(td) == "table" then
        -- teamData.id=0 表示"不在队伍"（退队后服务端推送的空队伍），不能当 teamId 退，否则 308 死循环
        if td.id and td.id ~= 0 then return td.id end
        if type(td.teamData) == "table" and td.teamData.id and td.teamData.id ~= 0 then
            return td.teamData.id
        end
    end
    return nil
end

local function clearTeamState()
    robot.delete("teamId")
    robot.delete("teamData")
    robot.delete("teamMemberCount")
    robot.delete("teamHeaderId")
end

local function leaveOnce(teamId)
    local msg = proto.create("Game.TeamLeaveC2S")
    proto.set_field(msg, "teamId", teamId)
    return network.tcp_request("logic", {cmd=5, act=2}, msg, "Game.TeamLeaveS2C")
end

local function cancelMatch()
    local msg = proto.create("Game.TeamCancelMatchC2S")
    network.tcp_request("logic", {cmd=5, act=21}, msg, "Game.TeamCancelMatchS2C")
end

function execute(r)
    local teamId = currentTeamId()
    if not teamId then
        return nil  -- 本地无 teamId；若服务端残留，后续 TeamCreate 会 314 暴露
    end

    local err = leaveOnce(teamId)
    if err == nil then
        clearTeamState()
        log.info("排位退队成功: teamId=" .. tostring(teamId))
        return nil
    end

    local code = tonumber(err.code)
    if code == 311 or code == 288 then
        -- 逻辑服可能先以 288 拦截 PS_Matching，团队服则返回 311；两者都先取消匹配。
        -- 队长/单排可取消后再退；队员无权取消，保留 teamId 等队长清理或下轮重试。
        local role = robot.get("rankedTeamRole")
        if role == "leader" or role == "solo" then
            cancelMatch()
            local err2 = leaveOnce(teamId)
            if err2 == nil then
                clearTeamState()
                log.info("排位退队成功(取消匹配后): teamId=" .. tostring(teamId))
                return nil
            end
            log.warn("排位退队仍失败(保留teamId): teamId=" .. tostring(teamId)
                .. " code=" .. tostring(err2.code))
        else
            log.warn("排位队员退队匹配中(保留teamId): teamId=" .. tostring(teamId)
                .. " code=" .. tostring(code))
        end
        return nil
    end

    if code == 308 then
        -- teamId 失配：本地 teamId 已与服端不符（战后队伍解散/服端 teamId=0，或被换到别的队）。
        -- 本地这个 teamId 已无意义，保留只会下轮重复 308——清掉，下轮 leave 跳过、由 TeamCreate/TeamJoin 重新写入。
        clearTeamState()
        log.warn("排位退队 teamId 失配(已清): teamId=" .. tostring(teamId) .. " code=308")
        return nil
    end

    -- 其它错误（网络等）：保留 teamId 下轮重试
    log.warn("排位退队失败(保留teamId): teamId=" .. tostring(teamId) .. " code=" .. tostring(code))
    return nil
end
