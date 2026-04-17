-- del_friend.lua: 删除好友（从 friendDetailList 中随机选一个）
local network = require("network")
local robot = require("robot")
local proto = require("proto")

function execute(r)
    local list = robot.get("friendDetailList")
    if not list or type(list) ~= "table" or #list == 0 then
        return 0
    end

    local friend = list[math.random(#list)]
    -- playerId 可能在 playerBase 子结构中
    local friendId = friend.playerId
        or (friend.playerBase and friend.playerBase.playerId)
        or (friend.base and friend.base.playerId)

    if not friendId then
        return 0
    end

    local msg = proto.create("Game.FriendDelC2S")
    proto.set_field(msg, "friendId", friendId)

    local code = network.send("logic", 15, 10, msg)
    return code
end
