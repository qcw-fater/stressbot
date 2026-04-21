-- bcc.lua: BCC XOR 折叠校验中间件
-- 对 body[offset:] 做 XOR 折叠，写入 header 的 keyField 字段
-- 对应 header.json 中 encrypt.keyField 配置

local bit = require("bit")

local function compute_bcc(data, offset)
    local bcc = 0
    for i = offset + 1, #data do
        bcc = bit.bxor(bcc, string.byte(data, i))
    end
    return bcc % 256
end

function middleware(ctx, next)
    if ctx.direction == "send" then
        local key = ctx.secret_key
        if #ctx.body > 0 and key and #key > 0 then
            local bcc = compute_bcc(ctx.body, ctx.encrypt_offset)
            ctx:set_header_field("bcc", bcc)
        end
    end
    next()
end
