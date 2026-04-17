-- get_mail_detail_data.lua: 获取邮件详情（从 mailList 中取第一封）
local network = require("network")
local robot = require("robot")
local proto = require("proto")
local utils = require("utils")

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

    local msg = proto.create("Game.MailGetDetailDataC2S")
    proto.set_field(msg, "gid", gid)

    local code = network.send("logic", 18, 2, msg)
    return code
end
