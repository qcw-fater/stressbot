-- request_player_data.lua: 发送 MainLoadOK(CMD=2,ACT=16) 并等待 LoginPlayerDataS2C(CMD=1,ACT=2)
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    local roleId = robot.get("roleId")

    -- 发送 MainLoadOkC2S (CMD=2, ACT=16)
    local msg = proto.create("Game.MainLoadOkC2S")
    local sendCode, sent = network.tcp_send("logic", {cmd=2, act=16}, msg)
    if sendCode ~= 0 then
        local failCode = sendCode or 3
        log.error("RequestPlayerData 发送 MainLoadOk 失败: service=logic route=2:16 roleId="
            .. tostring(roleId)
            .. " code=" .. tostring(failCode)
            .. " sent=" .. tostring(sent))
        return failCode, sent, 0
    end

    -- 等待 LoginPlayerDataS2C (CMD=1, ACT=2)，轮询 200 毫秒
    local resp, recv = network.tcp_listen("logic", {cmd=1, act=2}, "Game.LoginPlayerDataS2C", 30, 200)

    if not resp then
        log.error("RequestPlayerData 等待玩家数据超时: service=logic route=1:2 proto=Game.LoginPlayerDataS2C timeoutSec=30 pollMs=200 roleId="
            .. tostring(roleId)
            .. " sent=" .. tostring(sent)
            .. " recv=" .. tostring(recv))
        return 31, sent, recv  -- 31=LISTEN_TIMEOUT
    end

    -- 存储完整玩家数据
    local fieldMap = proto.get_field_map(resp)
    robot.set("playerData", fieldMap)

    -- 提取英雄 ID 列表
    -- 路径: loginHeroData -> HeroData -> heroList -> [].heroId
    local heroIds = {}
    local loginHero = fieldMap.loginHeroData
    if loginHero then
        local heroGetData = loginHero.HeroData
        if heroGetData and heroGetData.heroList then
            for _, hero in ipairs(heroGetData.heroList) do
                if hero.possess then
                    table.insert(heroIds, hero.heroId)
                end
            end
        end
    end

    -- 存储英雄列表（如果为空使用默认列表）
    if #heroIds == 0 then
        heroIds = {101, 102, 103, 104, 105, 106, 107, 108, 109, 110}
        log.warn("RequestPlayerData 未提取到英雄列表，使用默认英雄列表: roleId="
            .. tostring(roleId)
            .. " recv=" .. tostring(recv))
    end
    robot.set("heroIdList", heroIds)

    log.info("RequestPlayerData 成功: roleId=" .. tostring(roleId)
        .. " heroCount=" .. tostring(#heroIds)
        .. " recv=" .. tostring(recv))
    return 0, sent, recv
end
