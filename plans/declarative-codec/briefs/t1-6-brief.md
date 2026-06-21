# T1.6 Brief — 迁移产物：各连接 codec.json + 共享 errors.json

> 你是 implementer。先读本 brief。参考总纲 `plans/declarative-codec/00-master.md` §3.1（codec.json 完整示例）、§3.3（errors.json 格式）、决策 #8（每连接一份）；以及 `conf/adapter/codec.lua`（迁移源，验证字段布局一致）、`conf/adapter/error.lua`（errors 表迁移源）。
> 工作目录：worktree 根。**不要 git commit**。

## 目标

产出**生产迁移产物**（T4 将下发/加载这些文件），放到 `conf/adapter/`：

1. `conf/adapter/tcp_logic_codec.json` — `tcp:logic` 连接的 codec。
2. `conf/adapter/tcp_battle_codec.json` — `tcp:battle` 连接的 codec（与 tcp_logic 内容相同，TCP offset 0/0）。
3. `conf/adapter/udp_battle_codec.json` — `udp:battle` 连接的 codec（区别：encrypt offset `{encode:11, decode:0}`）。
4. `conf/adapter/errors.json` — 共享错误码描述（来自 `conf/adapter/error.lua` 的 errors 表）。

## codec.json 内容（逐字对齐总纲 §3.1 示例 + codec.lua 真值）

TCP 两份（tcp_logic / tcp_battle）内容相同，即总纲 §3.1 的完整示例（12B 头、routeKey `{cmd}:{act}`、gzip 阈值 2048 onlySmaller、xor_carry_rol rol=3 keyLen=32、bcc=xor8 produce region ciphered、offset `{encode:0, decode:0}`）：

```json
{
  "version": 1,
  "endianDefault": "le",
  "frame": { "headerSize": 12, "trailerSize": 0, "lengthIncludesHeader": false, "lengthIncludesTrailer": false },
  "header": [
    { "name": "bodyLen", "offset": 0,  "size": 4, "type": "u32", "endian": "le", "role": "length" },
    { "name": "errCode", "offset": 4,  "size": 2, "type": "u16", "role": "errorCode" },
    { "name": "cmd",     "offset": 6,  "size": 1, "type": "u8",  "role": "route" },
    { "name": "act",     "offset": 7,  "size": 1, "type": "u8",  "role": "route" },
    { "name": "index",   "offset": 8,  "size": 2, "type": "u16", "role": "value", "source": { "kind": "const", "value": 0 } },
    { "name": "flags",   "offset": 10, "size": 1, "type": "u8",  "role": "flags",
      "bits": [ { "name": "encrypted", "bit": 0 }, { "name": "compressed", "bit": 1 } ] },
    { "name": "bcc",     "offset": 11, "size": 1, "type": "u8",  "role": "checksumOut", "from": "enc.bcc" }
  ],
  "routeKeyTemplate": "{cmd}:{act}",
  "pipeline": [
    { "op": "compress", "name": "gz", "algo": "gzip", "flag": "compressed",
      "when": { "minBodyLen": 2048, "onlySmaller": true }, "onError": "fail" },
    { "op": "encrypt",  "name": "enc", "algo": "xor_carry_rol", "params": { "rol": 3 }, "keyLen": 32, "flag": "encrypted",
      "offset": { "encode": 0, "decode": 0 },
      "when": { "requireKey": true, "minBodyLen": 1, "guards": [ { "field": "cmd", "op": "neq", "value": 0 } ] },
      "produces": [ { "name": "bcc", "algo": "xor8", "region": "ciphered" } ],
      "onError": "fail" }
  ]
}
```

UDP 一份（udp_battle）：与上**完全相同**，**仅** encrypt 步的 `offset` 改为 `{ "encode": 11, "decode": 0 }`（UDP 发包前 11 字节明文、收包整体解）。

> 必须与 `conf/adapter/codec.lua` 的实际协议参数逐项核对一致：headerSize 12、各字段 offset/size/type、gzip 阈值 2048、xor_carry_rol rol=3、keyLen 32、bcc region、routeKey `cmd:act`。若总纲示例与 codec.lua 有出入，**以 codec.lua 真值为准**（T1.4/T1.5 对拍已证 codec.lua 是真值）并在报告里记下任何差异。

## errors.json 内容

来自 `conf/adapter/error.lua` 的 `errors` 表（`{[code]=中文描述}`）。转成扁平 JSON：`{ "<code>": "<中文描述>", ... }`（总纲 §3.3）。key 为字符串形式的 code。

- 逐条搬运（711 行映射）。可写一次性脚本从 error.lua 解析生成 errors.json，但**产物（errors.json）入库**到 `conf/adapter/`；脚本不入库（或放临时位置，勿提交）。
- 必须覆盖 error.lua 中所有 code→desc 对，不漏不重。

## 验证（用 T1.1-T1.5 已建好的引擎自验）

为每个 codec.json：

```go
schema, err := codec.LoadSchema("conf/adapter/tcp_logic_codec.json")  // Validate 通过
em, _ := codec.LoadErrorMap("conf/adapter/errors.json")
sc, err := codec.NewSchemaCodec(schema, em)                           // 编译通过（算法都在）
// 可选：encode/decode 对拍 codec.lua（复用 T1.4/T1.5 的对拍方法）证字节一致
```

写一个 `codec/migration_test.go`（或扩展现有 test）：LoadSchema + NewSchemaCodec 对 3 份 codec.json + errors.json 全部成功；udp_battle 的 enc step encOffset==11/decOffset==0；tcp 两份 offset 0/0。

## 关键约束

- 产物放 `conf/adapter/`（生产位置）；不删 `conf/adapter/codec.lua`、`error.lua`（T4 合流切换时删，本任务不动旧文件）。
- 不改 codec/ 或 adapter/ 代码（只产 conf 文件 + 可选 migration_test）。
- codec.json 字段名 camelCase，与 T1.1 schema 类型 JSON tag 一致；`LoadSchema` 能直接吃下。
- **不要 git commit。**

## 验收（self-review）

- 3 份 codec.json + 1 份 errors.json 在 `conf/adapter/`。
- 每份 codec.json 经 LoadSchema+Validate+NewSchemaCodec 成功（无错误）。
- udp_battle encOffset 11/decOffset 0；tcp 两份 0/0。
- errors.json 覆盖 error.lua 全部 code→desc。
- 字段布局与 codec.lua 一致（差异已在报告记录）。

## 报告

写完整报告到 `plans/declarative-codec/reports/t1-6-report.md`：产出文件、各 codec.json 与 codec.lua 的逐项核对结果（含任何差异）、errors.json 条目数、LoadSchema/NewSchemaCodec 自验结果、改动文件、self-review、concerns。
返回（<15 行）：Status、改动文件、一行验证摘要、concerns、报告路径。
