  local network = require("network")
  local proto = require("proto")
  local log = require("log")

  function execute(r)
      local ids = {4, 5, 7, 9}
      local allSend, allRecv = 0, 0

      for _, id in ipairs(ids) do
          local msg = proto.create("Game.MainGetLevelRewardC2S")
          proto.set_field(msg, "Id", id)

          local code, data, sent, recv = network.tcp_request("logic", { cmd = 2, act = 26 }, msg, "Game.MainGetLevelRewardC2S")

          allSend = allSend + (sent or 0)
          allRecv = allRecv + (recv or 0)

          if code ~= 0 or data == nil then
              log.error("解锁基础功能失败, id=" .. id)
          else
              log.debug("解锁基础功能成功, id=" .. id)
          end
      end

      return 0, allSend, allRecv
  end