-- watch_ob_tick.lua: 观战单帧轮询（对齐 sync_frame_data.lua 的 pull 模型）
-- 非阻塞探战斗结束推送(32:2 ObBattleEnd)：命中则置 obBattleEnded，由 watchEnd.breakCondition 退出；
-- obFrame(32:1) 由 listen_ob_frame.lua 的 on_message 计数，此处不处理（不入队，无需 drain）。
-- utils.sleep 释放 LState 锁，期间排队的 obFrame 回调在 Robot 单线程队列里排空，不会与 tick 死锁。
-- 真实观战时长上限由 conf/flow.json 的 watchEnd.loopCount × sleepMs 控制（与 syncLoop 同构）。
local network = require("network")
local robot = require("robot")
local utils = require("utils")

function execute(r)
    -- 非阻塞 pop 战斗结束推送；队列空返回 nil,nil（不是错误）
    local err, raw = network.try_tcp_listen("ob", {cmd=32, act=2})
    if err ~= nil then
        return err
    end
    if raw ~= nil then
        robot.set("obBattleEnded", 1)   -- breakCondition 生效，本轮 body 后退出
        return nil
    end
    utils.sleep(60)                      -- 与 syncLoop 同节奏（约 16fps）
    return nil
end
