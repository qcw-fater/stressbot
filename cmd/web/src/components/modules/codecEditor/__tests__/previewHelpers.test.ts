/**
 * T3 Batch-3 任务 B — previewHelpers 纯函数单测。
 *
 * 覆盖 deriveTransport（连接名→transport 推导）、collectRouteFields（route 字段提取）、
 * buildRouteMap（表单值→请求 route map）。纯函数无 React 依赖。
 */

import { describe, expect, it } from 'vitest';
import { buildRouteMap, collectRouteFields, deriveTransport, pruneRouteForm } from '../previewHelpers';
import type { CodecSchema, Field } from '@/types/codec';

describe('deriveTransport', () => {
  it("'tcp:logic' → 'tcp'", () => {
    expect(deriveTransport('tcp:logic')).toBe('tcp');
  });

  it("'udp:battle' → 'udp'", () => {
    expect(deriveTransport('udp:battle')).toBe('udp');
  });

  it('无冒号回退 tcp', () => {
    expect(deriveTransport('logic')).toBe('tcp');
  });

  it('未知 proto 回退 tcp', () => {
    expect(deriveTransport('http:logic')).toBe('tcp');
  });

  it('空/null/undefined 回退 tcp', () => {
    expect(deriveTransport('')).toBe('tcp');
    expect(deriveTransport(null)).toBe('tcp');
    expect(deriveTransport(undefined)).toBe('tcp');
  });

  it("冒号在首位（':logic'）回退 tcp", () => {
    expect(deriveTransport(':logic')).toBe('tcp');
  });
});

describe('collectRouteFields', () => {
  const base: CodecSchema = {
    version: 1,
    endianDefault: 'le',
    frame: { headerSize: 8, trailerSize: 0, lengthIncludesHeader: false, lengthIncludesTrailer: false },
    header: [],
    routeKeyTemplate: '',
    pipeline: [],
  };

  it('收集所有 role:"route" 字段，保序', () => {
    const schema: CodecSchema = {
      ...base,
      header: [
        { name: 'bodyLen', offset: 0, size: 4, type: 'u32', role: 'length' },
        { name: 'cmd', offset: 4, size: 1, type: 'u8', role: 'route' },
        { name: 'act', offset: 5, size: 1, type: 'u8', role: 'route' },
      ],
    };
    const r = collectRouteFields(schema);
    expect(r.map((f) => f.name)).toEqual(['cmd', 'act']);
  });

  it('无 route 字段返回空', () => {
    const schema: CodecSchema = {
      ...base,
      header: [{ name: 'bodyLen', offset: 0, size: 4, type: 'u32', role: 'length' }],
    };
    expect(collectRouteFields(schema)).toEqual([]);
  });

  it('跳过空名 route 字段', () => {
    const schema: CodecSchema = {
      ...base,
      header: [
        { name: '', offset: 0, size: 1, type: 'u8', role: 'route' },
        { name: 'cmd', offset: 1, size: 1, type: 'u8', role: 'route' },
      ],
    };
    expect(collectRouteFields(schema).map((f) => f.name)).toEqual(['cmd']);
  });

  it('同名 route 字段去重（保序）', () => {
    const schema: CodecSchema = {
      ...base,
      header: [
        { name: 'cmd', offset: 0, size: 1, type: 'u8', role: 'route' },
        { name: 'cmd', offset: 1, size: 1, type: 'u8', role: 'route' },
      ],
    };
    expect(collectRouteFields(schema).map((f) => f.name)).toEqual(['cmd']);
  });

  it('schema 为 null 返回空', () => {
    expect(collectRouteFields(null)).toEqual([]);
  });

  it('header 非数组安全降级为空', () => {
    const schema = { ...base, header: 'not-array' as unknown } as CodecSchema;
    expect(collectRouteFields(schema)).toEqual([]);
  });
});

describe('buildRouteMap', () => {
  it('纯整数串转 number', () => {
    expect(buildRouteMap({ cmd: '1', act: '2' })).toEqual({ cmd: 1, act: 2 });
  });

  it('负整数串转 number', () => {
    expect(buildRouteMap({ cmd: '-1' })).toEqual({ cmd: -1 });
  });

  it('空串剔除（不发给后端）', () => {
    expect(buildRouteMap({ cmd: '1', act: '' })).toEqual({ cmd: 1 });
  });

  it('空白串剔除（trim 后为空）', () => {
    expect(buildRouteMap({ cmd: '   ' })).toEqual({});
  });

  it('非数字串保留为 string（后端 routePreviewFloorInt 处理）', () => {
    expect(buildRouteMap({ cmd: 'abc' })).toEqual({ cmd: 'abc' });
  });

  it('混合 trim 后有效', () => {
    expect(buildRouteMap({ cmd: '  5  ', act: 'abc' })).toEqual({ cmd: 5, act: 'abc' });
  });

  it('空对象返回空对象', () => {
    expect(buildRouteMap({})).toEqual({});
  });

  it('"1e3" 不按整数解析（保留 string）', () => {
    // 正则 ^-?\d+$ 不匹配，保留 string
    expect(buildRouteMap({ cmd: '1e3' })).toEqual({ cmd: '1e3' });
  });
});

describe('pruneRouteForm', () => {
  const fields = (names: string[]): Field[] => names.map((name) => ({ name } as Field));

  it('保留仍存在的 route 字段键', () => {
    expect(pruneRouteForm({ cmd: '1', act: '2' }, fields(['cmd', 'act']))).toEqual({ cmd: '1', act: '2' });
  });

  it('剔除切连接后残留的旧字段键', () => {
    expect(pruneRouteForm({ cmd: '1', act: '2' }, fields(['cmd', 'sub']))).toEqual({ cmd: '1' });
  });

  it('routeFields 为空时清空所有', () => {
    expect(pruneRouteForm({ cmd: '1', act: '2' }, [])).toEqual({});
  });

  it('空表单返回空', () => {
    expect(pruneRouteForm({}, fields(['cmd']))).toEqual({});
  });

  it('只按键名过滤，不按值（空串值也保留，空串剔除交 buildRouteMap）', () => {
    expect(pruneRouteForm({ cmd: '' }, fields(['cmd']))).toEqual({ cmd: '' });
  });
});
