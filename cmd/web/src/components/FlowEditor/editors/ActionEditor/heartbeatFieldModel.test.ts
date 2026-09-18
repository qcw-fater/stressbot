/**
 * 心跳字段编辑规则。守的不变量：写回 codec JSON 的字段只带当前 (type, source) 用得上的参数，
 * 且浮点字段不会停在引擎拒绝的 source 上——这两条错了，压测跑得起来但心跳每 tick 报错，
 * 对端收不到保活包。
 */

import { describe, expect, it } from 'vitest';
import type { HeartbeatField } from '@/types/action';
import {
  changeHeartbeatFieldSource,
  changeHeartbeatFieldType,
  isSourceAllowedForType,
} from './heartbeatFieldModel';

describe('heartbeatFieldModel', () => {
  it('整型改浮点时 value 换成 floatValue 槽位', () => {
    const field: HeartbeatField = { type: 'u16', source: 'fixed', value: 7 };
    expect(changeHeartbeatFieldType(field, 'f32')).toEqual({ type: 'f32', source: 'fixed' });
  });

  it('浮点改整型时丢弃 floatValue', () => {
    const field: HeartbeatField = { type: 'f64', source: 'fixed', floatValue: 1.5 };
    expect(changeHeartbeatFieldType(field, 'i32')).toEqual({ type: 'i32', source: 'fixed' });
  });

  it('改成浮点时把不支持的 source 落到最接近的合法值', () => {
    const counter: HeartbeatField = { type: 'u32', source: 'counter', start: 1, step: 2 };
    expect(changeHeartbeatFieldType(counter, 'f32')).toEqual({ type: 'f32', source: 'fixed' });

    // stateCounter 仍是从 state 取值，落到 state 能留住 key
    const stateCounter: HeartbeatField = { type: 'u32', source: 'stateCounter', key: 'seq' };
    expect(changeHeartbeatFieldType(stateCounter, 'f64')).toEqual({
      type: 'f64',
      source: 'state',
      key: 'seq',
    });
  });

  it('浮点支持的 source 保持不变', () => {
    const field: HeartbeatField = { type: 'f32', source: 'state', key: 'ratio' };
    expect(changeHeartbeatFieldType(field, 'f64')).toEqual({
      type: 'f64',
      source: 'state',
      key: 'ratio',
    });
  });

  it('切换 source 丢弃上一个 source 的专属参数', () => {
    const field: HeartbeatField = { type: 'u32', source: 'randomInt', min: 1, max: 9 };
    expect(changeHeartbeatFieldSource(field, 'timestamp')).toEqual({
      type: 'u32',
      source: 'timestamp',
    });
    expect(changeHeartbeatFieldSource(field, 'state')).toEqual({ type: 'u32', source: 'state' });
  });

  it('同 source 内已填的参数不被清掉', () => {
    const field: HeartbeatField = { type: 'u32', source: 'counter', start: 5, step: 2 };
    expect(changeHeartbeatFieldSource(field, 'counter')).toEqual(field);
  });

  it('isSourceAllowedForType：只有浮点类型受限', () => {
    expect(isSourceAllowedForType('randomInt', 'u32')).toBe(true);
    expect(isSourceAllowedForType('randomInt', 'f32')).toBe(false);
    expect(isSourceAllowedForType('timestamp', 'f64')).toBe(false);
    expect(isSourceAllowedForType('state', 'f64')).toBe(true);
    expect(isSourceAllowedForType('fixed', 'f32')).toBe(true);
  });
});
