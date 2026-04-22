-- dispose_friend_request.lua: 处理好友申请（从 requestFriendList 读取 + 随机同意/拒绝）
local network = require("network")
local robot = require("robot")
local proto = require("proto")

function execute(r)
    local list = robot.get("requestFriendList")
    if not list or type(list) ~= "table" or #list == 0 then
        return 0
    end

    local friend = list[1]
    local friendId = friend.playerId
    if not friendId then
        return 0
    end

    local result = math.random(0, 1)  -- 0=拒绝, 1=同意

    local msg = proto.create("Game.FriendDisposeRequestC2S")
    proto.set_field(msg, "friendId", friendId)
    proto.set_field(msg, "result", result)

    local code = network.send("logic", {cmd=15, act=9}, msg)
    return code
end
