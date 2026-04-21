-- xor.lua: rotate-XOR 加解密中间件
-- 算法：(byte ^ key) + carry, rotate left 3，32 字节密钥循环

local bit = require("bit")

local CHUNK = 512

local function xor_encrypt(body, key, offset)
    if #body == 0 or #key == 0 then return body end
    if offset < 0 then offset = 0 end
    if offset > #body then offset = #body end

    local chunks = {}
    local ci = 1
    if offset > 0 then
        chunks[ci] = body:sub(1, offset)
        ci = ci + 1
    end

    local c = 0
    local buf = {}
    local bi = 1
    for i = offset + 1, #body do
        local k = ((i - offset - 1) % 32) + 1
        local mask = string.byte(key, k)
        local x = (bit.bxor(string.byte(body, i), mask) + c) % 256
        x = bit.rol(x, 3)
        c = x
        buf[bi] = string.char(x)
        bi = bi + 1
        if bi > CHUNK then
            chunks[ci] = table.concat(buf)
            ci = ci + 1
            buf = {}
            bi = 1
        end
    end
    if bi > 1 then
        chunks[ci] = table.concat(buf)
    end
    return table.concat(chunks)
end

local function xor_decrypt(body, key, offset)
    if #body == 0 or #key == 0 then return body end
    if offset < 0 then offset = 0 end
    if offset > #body then offset = #body end

    local chunks = {}
    local ci = 1
    if offset > 0 then
        chunks[ci] = body:sub(1, offset)
        ci = ci + 1
    end

    local c = 0
    local buf = {}
    local bi = 1
    for i = offset + 1, #body do
        local k = ((i - offset - 1) % 32) + 1
        local mask = string.byte(key, k)
        local enc = string.byte(body, i)
        local x = bit.bor(bit.rshift(enc, 3), bit.lshift(enc, 5)) % 256
        x = bit.bxor((x - c) % 256, mask)
        c = enc
        buf[bi] = string.char(x)
        bi = bi + 1
        if bi > CHUNK then
            chunks[ci] = table.concat(buf)
            ci = ci + 1
            buf = {}
            bi = 1
        end
    end
    if bi > 1 then
        chunks[ci] = table.concat(buf)
    end
    return table.concat(chunks)
end

function middleware(ctx, next)
    if ctx.direction == "send" then
        if #ctx.body > 0 and ctx.secret_key and #ctx.secret_key > 0 then
            ctx.body = xor_encrypt(ctx.body, ctx.secret_key, ctx.encrypt_offset)
            ctx:set_flag(0)
        end
        next()
    elseif ctx.direction == "recv" then
        if ctx:has_flag(0) and ctx.secret_key and #ctx.secret_key > 0 then
            ctx.body = xor_decrypt(ctx.body, ctx.secret_key, 0)
        end
        next()
    end
end
