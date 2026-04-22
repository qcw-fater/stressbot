-- conf/adapter/codec.lua
-- 当前服务器协议适配器（12 字节小端头）
--
-- 协议头字段布局（与 Server 二进制结构体 MessageHead 一致）：
--   offset  0 : uint32_le — body 长度（不含 header 12 字节）
--   offset  4 : uint16_le — 错误码
--   offset  6 : uint8     — cmd
--   offset  7 : uint8     — act
--   offset  8 : uint16_le — index（序号，压测中固定 0）
--   offset 10 : uint8     — flags（bit0=已加密，bit1=已压缩）
--   offset 11 : uint8     — bcc（明文数据的 XOR 校验字节，仅加密时设置）
--
-- 加密算法（NetEncrypt）：逐字节 stream cipher
--   x = (data[i] ^ key[k&31]) + carry
--   x = ROL8(x, 3)
--   carry = x
--   data[i] = x
--
-- 运行时约束：gopher-lua (Lua 5.1)，禁止使用 string.pack/unpack。

local bit  = require("bit")
local zlib = require("zlib")

-- ─── 协议常量 ────────────────────────────────────────────────────────────────
local HEADER_SIZE    = 12
local FLAG_ENCRYPT   = 1   -- bit0: 已加密
local FLAG_COMPRESS  = 2   -- bit1: 已压缩（GZIP）
local UDP_ENC_OFFSET = 11  -- UDP 数据前 11 字节保持明文
local GZIP_THRESHOLD = 256 -- body 超过此字节数才尝试 GZIP 压缩

-- ─── 元信息接口（Go 初始化时一次性调用，结果缓存到 Go 层）──────────────────

function header_size()
    return HEADER_SIZE
end

function body_length_info()
    return {
        offset          = 0,           -- header[0:4] 是 uint32_le body 长度
        field_type      = "uint32_le",
        includes_header = false         -- Len 字段是 body 长度，不含 header
    }
end

-- ─── 字节工具（Lua 5.1 兼容）────────────────────────────────────────────────

local function write_uint8(n)
    return string.char(bit.band(n, 0xFF))
end

local function write_uint16_le(n)
    return string.char(
        bit.band(n, 0xFF),
        bit.band(bit.rshift(n, 8), 0xFF)
    )
end

local function write_uint32_le(n)
    return string.char(
        bit.band(n, 0xFF),
        bit.band(bit.rshift(n, 8),  0xFF),
        bit.band(bit.rshift(n, 16), 0xFF),
        bit.band(bit.rshift(n, 24), 0xFF)
    )
end

local function read_uint8(s, offset)
    return string.byte(s, offset + 1)
end

local function read_uint16_le(s, offset)
    local lo = string.byte(s, offset + 1)
    local hi = string.byte(s, offset + 2)
    return lo + hi * 256
end

-- ─── 加密/解密（与 Server NetEncrypt/NetDecrypt 一致）───────────────────────

-- net_encrypt: 对 data[offset+1 .. #data] 用 32 字节 key 做 stream cipher 加密。
-- 返回 (加密后完整字节串, bcc 校验字节)。
-- bcc = XOR of all plaintext bytes in [offset+1, #data]。
local function net_encrypt(data, key, offset)
    if not key or #key ~= 32 or #data == 0 then return data, 0 end

    offset = offset or 0
    if offset < 0 then offset = 0 end

    local chunks = {}
    local bcc = 0

    -- 明文前缀
    if offset > 0 then
        chunks[#chunks + 1] = data:sub(1, offset)
    end

    -- 分段处理加密部分（避免 unpack 栈溢出）
    local chunk_size = 256
    local carry = 0
    local ki = 0

    local i = offset + 1
    while i <= #data do
        local j = math.min(i + chunk_size - 1, #data)
        local buf = {}
        for k = i, j do
            local plain_byte = string.byte(data, k)
            bcc = bit.bxor(bcc, plain_byte)

            local mask = string.byte(key, (ki % 32) + 1)
            local x = bit.band(plain_byte, 0xFF)
            x = bit.bxor(x, mask)
            x = x + carry
            x = bit.band(x, 0xFF)
            -- ROL8(x, 3)
            x = bit.bor(
                bit.band(bit.lshift(x, 3), 0xFF),
                bit.rshift(x, 5)
            )
            carry = x
            buf[#buf + 1] = x
            ki = ki + 1
        end
        chunks[#chunks + 1] = string.char(unpack(buf))
        i = j + 1
    end

    return table.concat(chunks), bit.band(bcc, 0xFF)
end

-- net_decrypt: 对 data[offset+1 .. #data] 用 32 字节 key 做 stream cipher 解密。
-- 返回解密后的完整字节串。
local function net_decrypt(data, key, offset)
    if not key or #key ~= 32 or #data == 0 then return data end

    offset = offset or 0
    if offset < 0 then offset = 0 end

    local chunks = {}

    -- 明文前缀
    if offset > 0 then
        chunks[#chunks + 1] = data:sub(1, offset)
    end

    local chunk_size = 256
    local carry = 0
    local ki = 0

    local i = offset + 1
    while i <= #data do
        local j = math.min(i + chunk_size - 1, #data)
        local buf = {}
        for k = i, j do
            local enc_byte = string.byte(data, k)

            -- ROR8(x, 3) = reverse of ROL8(x, 3)
            local x = bit.bor(
                bit.rshift(enc_byte, 3),
                bit.band(bit.lshift(enc_byte, 5), 0xFF)
            )
            x = bit.band(x, 0xFF)
            local mask = string.byte(key, (ki % 32) + 1)
            x = x - carry
            x = bit.band(x, 0xFF)
            x = bit.bxor(x, mask)

            carry = enc_byte
            buf[#buf + 1] = x
            ki = ki + 1
        end
        chunks[#chunks + 1] = string.char(unpack(buf))
        i = j + 1
    end

    return table.concat(chunks)
end

-- ─── 主接口函数 ───────────────────────────────────────────────────────────────

-- _do_encode: encode / encode_udp 共用的内部实现。
-- 处理顺序：GZIP 压缩 → 加密（与旧 middleware 链一致）
local function _do_encode(route, body, secret_key, encrypt_offset)
    local cmd = 0
    local act = 0
    if route ~= nil then
        cmd = math.floor(route.cmd or 0)
        act = math.floor(route.act or 0)
    end
    body = body or ""

    local flags = 0
    local bcc = 0
    local data = body

    -- Step 1: GZIP 压缩（body 超阈值时压缩）
    if #data >= GZIP_THRESHOLD then
        local ok, compressed = pcall(zlib.compress, data)
        if ok and compressed and #compressed < #data then
            data = compressed
            flags = bit.bor(flags, FLAG_COMPRESS)
        end
    end

    -- Step 2: 加密（仅当 cmd != 0 且有数据且有密钥时，与旧 SerializeAndEncrypt 一致）
    if cmd ~= 0 and #data > 0 and secret_key and #secret_key == 32 then
        flags = bit.bor(flags, FLAG_ENCRYPT)
        data, bcc = net_encrypt(data, secret_key, encrypt_offset or 0)
    end

    -- 构建 header：Len = body 长度（不含 header）
    local header = (
        write_uint32_le(#data) ..
        write_uint16_le(0)     ..
        write_uint8(cmd)       ..
        write_uint8(act)       ..
        write_uint16_le(0)     ..
        write_uint8(flags)     ..
        write_uint8(bcc)
    )

    return header .. data
end

-- encode(route, body, secret_key) → 完整 TCP 数据包字节
function encode(route, body, secret_key)
    return _do_encode(route, body, secret_key, 0)
end

-- encode_udp(route, body, secret_key) → 完整 UDP 数据包字节
function encode_udp(route, body, secret_key)
    return _do_encode(route, body, secret_key, UDP_ENC_OFFSET)
end

-- decode(data, secret_key) → (responseKey string, body string, headerErr number)
-- 处理顺序：解密 → GZIP 解压（与编码顺序相反）
function decode(data, secret_key)
    if #data < HEADER_SIZE then return "", "", 0 end

    local header_err = read_uint16_le(data, 4)
    local cmd        = read_uint8(data, 6)
    local act        = read_uint8(data, 7)
    local flags      = read_uint8(data, 10)

    -- body 是 header 之后的所有字节（Go 层已按 BodyLength 切好帧）
    local body = ""
    if #data > HEADER_SIZE then
        body = data:sub(HEADER_SIZE + 1)
    end

    -- Step 1: 解密
    if bit.band(flags, FLAG_ENCRYPT) ~= 0 and secret_key and #secret_key == 32 then
        body = net_decrypt(body, secret_key, 0)
    end

    -- Step 2: GZIP 解压
    if bit.band(flags, FLAG_COMPRESS) ~= 0 then
        local ok, decompressed = pcall(zlib.decompress, body)
        if ok and decompressed then
            body = decompressed
        end
    end

    local response_key = math.floor(cmd) .. ":" .. math.floor(act)
    return response_key, body, header_err
end

-- expected_response_key(route) → string
function expected_response_key(route)
    if route == nil then
        return "0:0"
    end
    local cmd = math.floor(route.cmd or 0)
    local act = math.floor(route.act or 0)
    return cmd .. ":" .. act
end
