-- world_chat.lua: 发送世界聊天消息
local network = require("network")
local proto = require("proto")

function execute(r)
    local chatInfo = proto.create("Game.ChatInfo")
    proto.set_field(chatInfo, "cType", 1)   -- CT_World
    proto.set_field(chatInfo, "content", "hello")
    proto.set_field(chatInfo, "ctType", 1)  -- CTT_NORMAL

    local msg = proto.create("Game.ChatSendC2S")
    proto.set_field(msg, "chatInfo", chatInfo)

    local code = network.send("logic", 13, 1, msg)
    return code
end
