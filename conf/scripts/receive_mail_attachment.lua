-- receive_mail_attachment.lua: 领取邮件附件（repeated int64 gids）
local network = require("network")
local robot = require("robot")
local proto = require("proto")

function execute(r)
    local mailList = robot.get("mailList")
    if not mailList or type(mailList) ~= "table" or #mailList == 0 then
        return 0
    end

    local mail = mailList[1]
    local gid = mail.mailUid or mail.gid
    if not gid then
        return 0
    end

    local msg = proto.create("Game.MailReceiveAttachmentC2S")
    proto.set_field(msg, "gids", {gid})

    local code = network.send("logic", {cmd=18, act=4}, msg)
    return code
end
