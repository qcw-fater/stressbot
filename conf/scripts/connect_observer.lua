-- connect_observer.lua: 连接观战服(observer) TCP + 密钥交换
-- observer 是独立进程（CMD=31 act3~8 / CMD=32 帧推送），地址由 31|2 取址返回的 state:obAddress 动态给出。
-- 复用 logic 连接同款的 (0,0) 种子握手：发空包取 32 字节密钥 → set_tcp_secret_key("ob", key)。
-- 心跳随 tcp_ob_codec.json 的 heartbeat 配置安装（route 31|8 ObPlayerTime，空包）；requireSecretKey=true 时密钥设置后启动。
-- ob 连接上的持久 listen（31|4 loading / 32|1 帧 / 32|2 结束）由流程节点 ConnectObserver 的 listenRefs 注册。
local network = require("network")
local robot = require("robot")
local log = require("log")

-- 去掉地址前可能携带的 scheme 前缀（tcp:// / ob:// 等），connect_tcp 需要 ip:port 形式
local function strip_scheme(addr)
    if type(addr) ~= "string" then return addr end
    return (addr:gsub("^%a+://", ""))
end

function execute(r)
    local rawAddr = robot.get("obAddress")
    local battleId = robot.get("obTargetBattleId") or 0
    if not rawAddr or rawAddr == "" then
        log.error("ConnectObserver 缺少观战服地址: battleId=" .. tostring(battleId))
        return robot.error(41, "ConnectObserver 缺少观战服地址: battleId=" .. tostring(battleId))  -- 41=ADDR_EMPTY
    end

    local obAddress = strip_scheme(rawAddr)
    log.info("连接观战服: address=" .. obAddress .. " battleId=" .. tostring(battleId))

    local err = network.connect_tcp("ob", obAddress)
    if err then
        log.error("连接观战服失败: address=" .. obAddress
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err
    end

    -- 发空包获取密钥并设置到连接（与 logic/battle 同款握手）
    local err, keyBody = network.tcp_request("ob", {cmd=0, act=0})
    if err then
        log.error("观战服密钥交换失败: address=" .. obAddress
            .. " code=" .. tostring(err.code) .. " detail=" .. tostring(err.detail))
        return err  -- 透传底层 err table
    end
    if not keyBody or #keyBody == 0 then
        -- observer 应与 logic 同为加密连接；空响应=协议异常。若实测 observer 为明文，则需另配 requireSecretKey:false 的 codec。
        log.error("观战服密钥交换响应为空: address=" .. obAddress)
        return robot.error(12, "ob 密钥交换响应为空: address=" .. obAddress)  -- 12=PARSE_FAILED
    end
    network.set_tcp_secret_key("ob", keyBody)

    log.info("观战服连接成功: address=" .. obAddress
        .. " battleId=" .. tostring(battleId)
        .. " hasSecretKey=true 心跳(31|8)由协议配置在密钥设置后启动(10s)")
    return nil
end
