/**
 * routeKeyResolver 单测：computeRouteKey 纯函数 + loadRouteKeyTemplates（mock 本地存储）。
 *
 * routeKey 使用协议配置的 routeKeyTemplate 代入 route 字段值计算。
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { computeRouteKey, pseudoRouteKey, loadRouteKeyTemplates } from './routeKeyResolver';

// 隔离 resourcesStore 的 listCodecFiles / codecFileNameToConnName，避免触碰真实本地数据库。
vi.mock('@/services/resourcesStore', () => ({
  listCodecFiles: vi.fn(),
}));
vi.mock('@/services/taskResourceDiff', () => ({
  codecFileNameToConnName: (name: string): string => {
    // 镜像真实实现：去 _codec.json，首个 _ 换 :
    const stripped = name.endsWith('_codec.json') ? name.slice(0, -'_codec.json'.length) : name;
    const idx = stripped.indexOf('_');
    if (idx < 0) return stripped;
    return `${stripped.slice(0, idx)}:${stripped.slice(idx + 1)}`;
  },
}));

// 动态导入以拿到被 mock 的引用。
import { listCodecFiles } from '@/services/resourcesStore';
import type { ResourceFile } from '@/services/resourcesStore';

const mockedListCodecFiles = vi.mocked(listCodecFiles);

function makeCodecFile(name: string, routeKeyTemplate: string | null): ResourceFile {
  const content = routeKeyTemplate === null
    ? '{"version":1,"endianDefault":"le","frame":{"headerSize":12},"header":[],"pipeline":[]}'
    : `{"version":1,"endianDefault":"le","frame":{"headerSize":12},"header":[],"routeKeyTemplate":${JSON.stringify(routeKeyTemplate)},"pipeline":[]}`;
  return { name, content, size: content.length, uploadedAt: '2026-06-20T00:00:00.000Z' };
}

describe('computeRouteKey', () => {
  it('单占位正常代入', () => {
    expect(computeRouteKey('{cmd}', { cmd: 1 })).toBe('1');
  });

  it('多占位正常代入（{cmd}:{act}）', () => {
    expect(computeRouteKey('{cmd}:{act}', { cmd: 1, act: 2 })).toBe('1:2');
  });

  it('占位字段缺失 → null（不可解析，flow 数据问题）', () => {
    expect(computeRouteKey('{cmd}:{act}', { cmd: 1 })).toBeNull();
  });

  it('非对象 route → null', () => {
    expect(computeRouteKey('{cmd}', null)).toBeNull();
    expect(computeRouteKey('{cmd}', undefined)).toBeNull();
    expect(computeRouteKey('{cmd}', 'foo')).toBeNull();
    expect(computeRouteKey('{cmd}', 123)).toBeNull();
    expect(computeRouteKey('{cmd}', [1, 2])).toBeNull();
  });

  it('无占位模板原样返回', () => {
    expect(computeRouteKey('static', { cmd: 1 })).toBe('static');
    expect(computeRouteKey('', { cmd: 1 })).toBe('');
  });

  it('数值与字符串值都字符串化代入', () => {
    expect(computeRouteKey('{cmd}:{name}', { cmd: 7, name: 'login' })).toBe('7:login');
  });

  it('占位重复出现也全部代入', () => {
    expect(computeRouteKey('{cmd}-{cmd}', { cmd: 3 })).toBe('3-3');
  });

  it('占位字段值为 null/undefined → null（缺失语义）', () => {
    expect(computeRouteKey('{cmd}', { cmd: null })).toBeNull();
    expect(computeRouteKey('{cmd}', { cmd: undefined })).toBeNull();
  });
});

describe('pseudoRouteKey（旧伪实现，降级用）', () => {
  it('键序不同仍稳定（向后兼容旧 routeKey 行为）', () => {
    expect(pseudoRouteKey({ cmd: 1, act: 2 })).toBe(pseudoRouteKey({ act: 2, cmd: 1 }));
  });

  it('不同值产生不同 key', () => {
    expect(pseudoRouteKey({ cmd: 1, act: 2 })).not.toBe(pseudoRouteKey({ cmd: 1, act: 3 }));
  });

  it('空对象 → "{}"', () => {
    expect(pseudoRouteKey({})).toBe('{}');
  });
});

describe('loadRouteKeyTemplates', () => {
  beforeEach(() => {
    mockedListCodecFiles.mockReset();
  });

  it('正常加载多份 codec，返回 server → template Map', async () => {
    mockedListCodecFiles.mockResolvedValue([
      makeCodecFile('tcp_logic_codec.json', '{cmd}:{act}'),
      makeCodecFile('udp_battle_codec.json', '{cmd}'),
    ]);
    const map = await loadRouteKeyTemplates();
    expect(map.get('tcp:logic')).toBe('{cmd}:{act}');
    expect(map.get('udp:battle')).toBe('{cmd}');
    expect(map.size).toBe(2);
  });

  it('跳过 JSON 解析失败的坏文件', async () => {
    mockedListCodecFiles.mockResolvedValue([
      { name: 'tcp_logic_codec.json', content: '{not json', size: 9, uploadedAt: '' },
      makeCodecFile('udp_battle_codec.json', '{cmd}'),
    ]);
    const map = await loadRouteKeyTemplates();
    expect(map.size).toBe(1);
    expect(map.has('tcp:logic')).toBe(false);
    expect(map.get('udp:battle')).toBe('{cmd}');
  });

  it('跳过缺 routeKeyTemplate 的文件', async () => {
    mockedListCodecFiles.mockResolvedValue([
      makeCodecFile('tcp_logic_codec.json', null),
      makeCodecFile('udp_battle_codec.json', '{cmd}'),
    ]);
    const map = await loadRouteKeyTemplates();
    expect(map.size).toBe(1);
    expect(map.has('tcp:logic')).toBe(false);
  });

  it('跳过 routeKeyTemplate 非字符串的文件', async () => {
    const content = '{"version":1,"routeKeyTemplate":123,"header":[]}';
    mockedListCodecFiles.mockResolvedValue([
      { name: 'tcp_logic_codec.json', content, size: content.length, uploadedAt: '' },
    ]);
    const map = await loadRouteKeyTemplates();
    expect(map.size).toBe(0);
  });

  it('空列表 → 空 Map', async () => {
    mockedListCodecFiles.mockResolvedValue([]);
    const map = await loadRouteKeyTemplates();
    expect(map.size).toBe(0);
  });
});
