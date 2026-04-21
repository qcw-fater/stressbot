-- example_checksum.lua: 示例 Lua 中间件
-- 对 body[offset:] 计算简单累加校验和，写入 header 的 bcc 字段。
-- 仅供演示，实际使用时可通过 require("mw").bcc(data, offset) 调用 Go 内置算法。

function middleware(ctx, next)
    if ctx.direction == "send" then
        local body = ctx.body
        local offset = ctx.encrypt_offset
        if #body > offset then
            local sum = 0
            for i = offset + 1, #body do
                sum = (sum + string.byte(body, i)) % 256
            end
            ctx:set_header_field("bcc", sum)
        end
    end
    next()
end
