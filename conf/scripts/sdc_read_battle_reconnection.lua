-- sdc_read_battle_reconnection.lua: 从登录玩家数据中读取服务端战斗重连标记。
-- 只负责状态提取；连接、登录和恢复分支均由 flow 声明式节点编排。
local robot = require("robot")
local proto = require("proto")

function execute(r)
    local playerData = robot.get_view("playerData")
    local reconnecting = proto.get_field(playerData, "isBattleReconnection") == true
    robot.set("sdcLoginBattleReconnection", reconnecting)
    return nil
end
