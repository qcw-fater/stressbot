/**
 * T3 Batch-1 任务 A — validateCodecSchema 单测。
 *
 * 逐条镜像 codec/schema.go 的 Validate。断言命中关键中文子串（不靠「数组非空」蒙混）。
 */

import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { validateCodecSchema } from '../resourcesStore';
import type { CodecSchema } from '@/types/codec';

// 读真实 codec.json（一份 PASS 样本），断言空错误。
// vitest 从 cmd/web 运行；conf/adapter 在仓库根。
const realCodecPath = resolve(process.cwd(), '../../conf/adapter/tcp_logic_codec.json');
const realCodecText = readFileSync(realCodecPath, 'utf-8');

/** 构造一份最小合法 schema 作为 mutate 起点。 */
function validSchema(): CodecSchema {
  return JSON.parse(realCodecText) as CodecSchema;
}

describe('validateCodecSchema — PASS 样本', () => {
  it('真实 tcp_logic_codec.json → 空错误数组', () => {
    const errs = validateCodecSchema(realCodecText);
    expect(errs).toEqual([]);
  });
});

describe('validateCodecSchema — base 校验', () => {
  it('坏 JSON → 「不是合法 JSON」', () => {
    const errs = validateCodecSchema('{ not json');
    expect(errs.some((e) => e.includes('不是合法 JSON'))).toBe(true);
  });

  it('version != 1', () => {
    const s = validSchema();
    s.version = 2;
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('version 必须为 1'))).toBe(true);
  });

  it('endianDefault 非法', () => {
    const s = validSchema();
    s.endianDefault = 'middle';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('endianDefault 必须为 le 或 be'))).toBe(true);
  });

  it('headerSize <= 0', () => {
    const s = validSchema();
    s.frame.headerSize = 0;
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('headerSize 必须大于 0'))).toBe(true);
  });

  it('trailerSize < 0', () => {
    const s = validSchema();
    s.frame.trailerSize = -1;
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('trailerSize 不能为负'))).toBe(true);
  });

  it('routeKeyTemplate 为空', () => {
    const s = validSchema();
    s.routeKeyTemplate = '   ';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('routeKeyTemplate 不能为空'))).toBe(true);
  });
});

describe('validateCodecSchema — header 字段', () => {
  it('字段名重复', () => {
    const s = validSchema();
    s.header[1].name = s.header[0].name; // errCode → bodyLen
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('重复'))).toBe(true);
  });

  it('offset 为负', () => {
    const s = validSchema();
    s.header[0].offset = -1;
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('offset 不能为负'))).toBe(true);
  });

  it('size <= 0', () => {
    const s = validSchema();
    s.header[0].size = 0;
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('size 必须大于 0'))).toBe(true);
  });

  it('物理区间越界（offset+size > headerSize）', () => {
    const s = validSchema();
    // bcc（u8，size=1，原本 offset=11）挪到 offset=12 → offset+size=13 > headerSize=12，
    // 只触发「越界」分支；不破坏 type u8 的 size 约束（仍 size=1）。
    s.header[6].offset = 12;
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('越界'))).toBe(true);
  });

  it('type u32 的 size 必须 = 4', () => {
    const s = validSchema();
    s.header[0].type = 'u32';
    s.header[0].size = 3;
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('type') && e.includes('size 必须为'))).toBe(true);
  });

  it('未知 type', () => {
    const s = validSchema();
    s.header[0].type = 'u128';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('未知 type'))).toBe(true);
  });

  it('endian 非法', () => {
    const s = validSchema();
    s.header[0].endian = 'middle';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('endian 必须为 le 或 be'))).toBe(true);
  });

  it('未知 role', () => {
    const s = validSchema();
    s.header[0].role = 'mystery';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('未知 role'))).toBe(true);
  });

  it('物理区间重叠（两字段抢同一字节）', () => {
    const s = validSchema();
    // errCode offset=4 size=2；把 cmd (offset=6 size=1) 挪到 offset=5 → 与 errCode 重叠
    s.header[2].offset = 5;
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('物理区间重叠'))).toBe(true);
  });

  it('缺 length role', () => {
    const s = validSchema();
    s.header[0].role = 'value'; // bodyLen 原本是 length
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('缺少 role:"length"'))).toBe(true);
  });

  it('多于 1 个 length', () => {
    const s = validSchema();
    s.header[1].role = 'length'; // errCode 也变 length
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('role:"length" 字段') && e.includes('必须有且仅有 1 个'))).toBe(true);
  });

  it('缺 route role', () => {
    const s = validSchema();
    s.header[2].role = 'reserved'; // cmd 从 route 改掉
    s.header[3].role = 'reserved'; // act 也改掉
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('缺少 role:"route"'))).toBe(true);
  });

  it('flags bit 超出 [0, size*8)', () => {
    const s = validSchema();
    // flags 字段在 header[5]，size=1 → bit 必须在 [0,8)。把 bit 改为 9
    s.header[5].bits![0].bit = 9;
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('超出'))).toBe(true);
  });

  it('flags bit 重复', () => {
    const s = validSchema();
    s.header[5].bits![1].bit = 0; // 与第一个 bit=0 重复
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('bit') && e.includes('重复'))).toBe(true);
  });

  it('flags 命名位名称为空', () => {
    const s = validSchema();
    // header[5]=flags，bits[0].name 清空 → 命中 schema.go validateFlagBits 行 346-347
    s.header[5].bits![0].name = '';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('名称为空'))).toBe(true);
  });

  it('flags 命名位名称重复', () => {
    const s = validSchema();
    // 两个不同 bit（0 与 2）同名 → 命中 schema.go validateFlagBits 行 348-349
    s.header[5].bits![1].bit = 2; // 先错开 bit，避免触发 bit 重复
    s.header[5].bits![1].name = s.header[5].bits![0].name; // compressed → encrypted
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('命名位') && e.includes('重复'))).toBe(true);
  });

  it('checksumOut from 不匹配 <step>.<output>', () => {
    const s = validSchema();
    s.header[6].from = 'no-dot-format';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('from') && e.includes('不合法'))).toBe(true);
  });

  it('value source.kind 未知', () => {
    const s = validSchema();
    s.header[4].source!.kind = 'mystery';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('source.kind') && e.includes('未知'))).toBe(true);
  });

  it('value source.kind 不支持（state，v1.1）', () => {
    const s = validSchema();
    s.header[4].source!.kind = 'state';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('不支持'))).toBe(true);
  });
});

describe('validateCodecSchema — routeKeyTemplate 占位', () => {
  it('占位 {x} 不指向任何 route 字段', () => {
    const s = validSchema();
    s.routeKeyTemplate = '{cmd}:{nonexistent}';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('routeKeyTemplate 占位') && e.includes('role:"route"'))).toBe(true);
  });
});

describe('validateCodecSchema — pipeline', () => {
  it('step name 重复', () => {
    const s = validSchema();
    s.pipeline[1].name = s.pipeline[0].name; // enc → gz
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('pipeline 步骤 name') && e.includes('重复'))).toBe(true);
  });

  it('未知 op', () => {
    const s = validSchema();
    s.pipeline[0].op = 'scramble';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('未知 op'))).toBe(true);
  });

  it('algo 为空', () => {
    const s = validSchema();
    s.pipeline[0].algo = '';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('algo 不能为空'))).toBe(true);
  });

  it('onError 非法', () => {
    const s = validSchema();
    s.pipeline[0].onError = 'retry';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('onError') && e.includes('不合法'))).toBe(true);
  });

  it('produces region 非法', () => {
    const s = validSchema();
    s.pipeline[1].produces![0].region = 'galaxy';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('region') && e.includes('不合法'))).toBe(true);
  });

  it('encrypt offset.encode 为负', () => {
    const s = validSchema();
    s.pipeline[1].offset!.encode = -1;
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('offset.encode 不能为负'))).toBe(true);
  });

  it('over.kind 非法', () => {
    const s = validSchema();
    s.pipeline[0].over = { kind: 'planet' };
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('over.kind') && e.includes('不合法'))).toBe(true);
  });

  it('over kind=range 区间非法（rangeEnd < rangeStart）', () => {
    const s = validSchema();
    s.pipeline[0].over = { kind: 'range', rangeStart: 10, rangeEnd: 5 };
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('over range 区间非法'))).toBe(true);
  });

  it('when.appliesWith 指向不存在 step', () => {
    const s = validSchema();
    s.pipeline[0].when!.appliesWith = 'ghost-step';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('when.appliesWith') && e.includes('不存在的 step'))).toBe(true);
  });

  it('guard op 非法', () => {
    const s = validSchema();
    s.pipeline[1].when!.guards![0].op = 'matches';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('guard') && e.includes('op') && e.includes('不合法'))).toBe(true);
  });
});

describe('validateCodecSchema — heartbeat', () => {
  it('连接级 protobuf 心跳合法', () => {
    const s = validSchema();
    s.heartbeat = {
      intervalMs: 5000,
      route: { cmd: 1, act: 2 },
      c2sProto: 'X.Heartbeat',
      bindings: [{ field: 'seq', type: 'fixed', value: 1 }],
    };
    expect(validateCodecSchema(JSON.stringify(s))).toEqual([]);
  });

  it('连接级 raw-binary 心跳合法', () => {
    const s = validSchema();
    s.heartbeat = {
      intervalMs: 5000,
      route: { cmd: 1, act: 2 },
      heartbeatFields: [{ type: 'u32', source: 'counter', start: 1, step: 1 }],
      skipWhenMissing: true,
      requireSecretKey: true,
    };
    expect(validateCodecSchema(JSON.stringify(s))).toEqual([]);
  });

  it('requireSecretKey 必须是布尔值', () => {
    const s = validSchema();
    s.heartbeat = { intervalMs: 5000, route: { cmd: 1, act: 2 }, requireSecretKey: 'yes' as unknown as boolean };
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('heartbeat.requireSecretKey 必须是布尔值'))).toBe(true);
  });

  it('intervalMs 必须大于 0', () => {
    const s = validSchema();
    s.heartbeat = { intervalMs: 0, route: { cmd: 1, act: 2 } };
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('heartbeat.intervalMs 必须大于 0'))).toBe(true);
  });

  it('按 routeKeyTemplate 校验 route 字段', () => {
    const s = validSchema();
    s.heartbeat = { intervalMs: 5000, route: { cmd: 1 } };
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('heartbeat.route 缺少字段 "act"'))).toBe(true);
  });

  it('c2sProto 与 heartbeatFields 互斥', () => {
    const s = validSchema();
    s.heartbeat = {
      intervalMs: 5000,
      route: { cmd: 1, act: 2 },
      c2sProto: 'X.Heartbeat',
      heartbeatFields: [{ type: 'u16', source: 'fixed', value: 0 }],
    };
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('不能同时配置 c2sProto 与 heartbeatFields'))).toBe(true);
  });

  it('bindings 只能配合 c2sProto 使用', () => {
    const s = validSchema();
    s.heartbeat = { intervalMs: 5000, route: { cmd: 1, act: 2 }, bindings: [{ field: 'seq', type: 'fixed', value: 1 }] };
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('heartbeat.bindings 只能在配置 c2sProto 时使用'))).toBe(true);
  });

  it('校验 heartbeatFields 字段参数', () => {
    const s = validSchema();
    s.heartbeat = { intervalMs: 5000, route: { cmd: 1, act: 2 }, heartbeatFields: [{ type: 'f32', source: 'randomInt', min: 1, max: 2 }] };
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('浮点字段仅支持 fixed/state source'))).toBe(true);
  });
});

describe('validateCodecSchema — pipeline↔header 引用', () => {
  it('flag 引用未在任何 flags 字段命名位中声明', () => {
    const s = validSchema();
    s.pipeline[0].flag = 'nonexistent-flag';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('flag') && e.includes('未在任何 role:"flags"'))).toBe(true);
  });

  it('带 when 的 step 未绑定 flag', () => {
    const s = validSchema();
    s.pipeline[0].flag = ''; // gz 原本绑 compressed，去掉
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('带有 when 但未绑定 flag'))).toBe(true);
  });

  it('checksumOut from 指向不存在的 step', () => {
    const s = validSchema();
    s.header[6].from = 'ghost.bcc';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('from') && e.includes('不存在的 step'))).toBe(true);
  });

  it('checksumOut from 指向存在 step 但不存在 produce', () => {
    const s = validSchema();
    s.header[6].from = 'enc.nonexistent';
    const errs = validateCodecSchema(JSON.stringify(s));
    expect(errs.some((e) => e.includes('不存在的 produce'))).toBe(true);
  });
});
