-- request_player_data.lua: 发送 MainLoadOK(CMD=2,ACT=16) 并等待 LoginPlayerDataS2C(CMD=1,ACT=2)
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local log = require("log")

function execute(r)
    local roleId = robot.get("roleId")

    -- 发送 MainLoadOkC2S (CMD=2, ACT=16)，等待 LoginPlayerDataS2C (CMD=1, ACT=2)
    local msg = proto.create("Game.MainLoadOkC2S")
    local err, resp = network.tcp_request_route(
        "logic",
        {cmd=2, act=16},
        {cmd=1, act=2},
        msg,
        "Game.LoginPlayerDataS2C",
        30
    )

    if err then
        log.error("RequestPlayerData 请求玩家数据失败: service=logic requestRoute=2:16 responseRoute=1:2 proto=Game.LoginPlayerDataS2C roleId="
            .. tostring(roleId)
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    -- 存储完整玩家数据：直接存消息（框架侧以不可变引用常驻，免整树展开；
    -- 读路径 robot.get_path 惰性取值，嵌套写 robot.set_path 走写时物化）
    robot.set("playerData", resp)

    -- 提取英雄 ID 列表
    -- 路径: loginHeroData -> HeroData -> heroList -> [].heroId
    local heroIds = {}
    local heroList = proto.get_path(resp, "loginHeroData.HeroData.heroList")
    if type(heroList) == "table" then
        for _, hero in ipairs(heroList) do
            if hero.possess then
                table.insert(heroIds, hero.heroId)
            end
        end
    end

    -- 存储英雄列表（如果为空使用默认列表）
    if #heroIds == 0 then
        heroIds = {101, 102, 103, 104, 105, 106, 107, 108, 109, 110}
        log.warn("RequestPlayerData 未提取到英雄列表，使用默认英雄列表: roleId="
            .. tostring(roleId))
    end
    robot.set("heroIdList", heroIds)

    log.info("RequestPlayerData 成功: roleId=" .. tostring(roleId)
        .. " heroCount=" .. tostring(#heroIds))
    return nil
end
