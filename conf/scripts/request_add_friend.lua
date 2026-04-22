-- request_add_friend.lua: 申请添加好友（从 recommendFriendList 读取 friendId）
local network = require("network")
local robot = require("robot")
local proto = require("proto")

function execute(r)
    local list = robot.get("recommendFriendList")
    if not list or type(list) ~= "table" or #list == 0 then
        return 0
    end

    local friend = list[1]
    local friendId = friend.playerId
    if not friendId then
        return 0
    end

    local msg = proto.create("Game.FriendAddRequestC2S")
    proto.set_field(msg, "friendId", friendId)

    local code = network.send("logic", {cmd=15, act=7}, msg)
    return code
end
