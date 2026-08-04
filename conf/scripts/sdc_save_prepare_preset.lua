-- 声明式绑定暂不支持把普通 table 递归绑定为 message；本脚本只构造并提交一次预设保存请求。
local network = require("network")
local robot = require("robot")
local proto = require("proto")

local function build_ext(data)
    if type(data) ~= "table" then return nil end
    local ext = proto.create("Game.SDCItemExtInfo")
    proto.set_field(ext, "sdcType", data.sdcType or 0)
    proto.set_field(ext, "sdcSubType", data.sdcSubType or 0)
    proto.set_field(ext, "durability", data.durability or 0)
    proto.set_field(ext, "maxDurability", data.maxDurability or 0)
    proto.set_field(ext, "capacity", data.capacity or 0)
    proto.set_field(ext, "value", data.value or 0)
    proto.set_field(ext, "number", data.number or 0)
    if type(data.affixIds) == "table" and #data.affixIds > 0 then
        proto.set_field(ext, "affixIds", data.affixIds)
    end
    proto.set_field(ext, "bindHeroId", data.bindHeroId or 0)
    proto.set_field(ext, "originalValue", data.originalValue or 0)
    return ext
end

local function build_preset(data)
    local preset = proto.create("Game.SDCPresetData")
    proto.set_field(preset, "slotIndex", data.slotIndex or 0)
    proto.set_field(preset, "name", data.name or "")
    local slots = {}
    for _, source in ipairs(data.slots or {}) do
        local slot = proto.create("Game.SDCPresetSlot")
        proto.set_field(slot, "containerType", source.containerType or 0)
        proto.set_field(slot, "containerDetail", source.containerDetail or 0)
        proto.set_field(slot, "itemId", source.itemId or 0)
        local ext = build_ext(source.ext)
        if ext then proto.set_field(slot, "ext", ext) end
        proto.set_field(slot, "expired", source.expired == true)
        slots[#slots + 1] = slot
    end
    if #slots > 0 then proto.set_field(preset, "slots", slots) end
    proto.set_field(preset, "amplifierPackSlots", data.amplifierPackSlots or 0)
    proto.set_field(preset, "medicinePackSlots", data.medicinePackSlots or 0)
    proto.set_field(preset, "keyChainPackSlots", data.keyChainPackSlots or 0)
    return preset
end

function execute(r)
    local params = robot.get("sdcPrepareActionParams") or {}
    if type(params.preset) ~= "table" then
        return robot.error(54, "保存战备预设缺少 preset 参数")
    end
    local msg = proto.create("Game.SdcSavePresetC2S")
    proto.set_field(msg, "slotIndex", params.slotIndex or 1)
    proto.set_field(msg, "name", "压测方案")
    proto.set_field(msg, "preset", build_preset(params.preset))
    proto.set_field(msg, "overwrite", true)
    local err = network.tcp_request("logic", {cmd = 45, act = 125}, msg, "Game.SdcSavePresetS2C")
    if err then return err end
    robot.set("sdcPrepareActionErrorCode", 0)
    return nil
end
