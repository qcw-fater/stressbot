/**
 * protoSync 单测：
 *   - missingProtoImports：纯静态 import 依赖完整性扫描（google/* 跳过、basename 匹配、去重排序、注释不误判）；
 *   - syncProtosToIdb：基线不可用跳过 / 已有不覆盖 / 内容拉取失败收集 / 混合场景。
 *
 * 不依赖真实浏览器本地数据库 —— 用 vi.mock 把 resourcesStore 替成内存 Map，fetch 替成 stub。
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// ---- mock ----
const idb = new Map<string, { name: string; content: string }>();

vi.mock('../resourcesStore', () => ({
  getProto: vi.fn(async (name: string) => idb.get(name)),
  addProtoFromBaseline: vi.fn(async (name: string, content: string) => {
    const file = { name, content, size: content.length, uploadedAt: '2026-01-01T00:00:00Z', baseHash: 'sha256:test' };
    idb.set(name, file);
    return file;
  }),
}));

// ---- import after mock ----
import { missingProtoImports, syncProtosToIdb } from '../protoSync';

const fetchMock = vi.fn();

beforeEach(() => {
  idb.clear();
  fetchMock.mockReset();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function proto(name: string, content: string) {
  return { name, content, size: content.length, uploadedAt: '2026-01-01T00:00:00Z' };
}

/** 仿真一个声明了若干 import 的 proto 文件头。 */
function withImports(...imports: string[]): string {
  return ['syntax = "proto3";', 'package Game;', '', ...imports.map((i) => `import "${i}";`), ''].join('\n');
}

// ---------------- missingProtoImports（纯函数）----------------

describe('missingProtoImports', () => {
  it('空集合 → 无缺失', () => {
    expect(missingProtoImports([])).toEqual([]);
  });

  it('所有 import 目标都在集合内 → 无缺失', () => {
    const protos = [
      proto('custom_activity.proto', withImports('custom_task.proto', 'common.proto')),
      proto('custom_task.proto', withImports('enum.proto')),
      proto('common.proto', withImports('enum.proto', 'player.proto')),
      proto('enum.proto', 'syntax = "proto3";\n'),
      proto('player.proto', 'syntax = "proto3";\n'),
    ];
    expect(missingProtoImports(protos)).toEqual([]);
  });

  it('缺 custom_task.proto → 报该项（复现后端编译失败的实际场景）', () => {
    const protos = [
      proto('custom_activity.proto', withImports('custom_task.proto', 'common.proto')),
      proto('common.proto', withImports('enum.proto')),
      proto('enum.proto', 'syntax = "proto3";\n'),
    ];
    expect(missingProtoImports(protos)).toEqual(['custom_task.proto']);
  });

  it('google/* well-known types 跳过（后端 WithStandardImports 提供）', () => {
    const protos = [proto('a.proto', withImports('google/protobuf/empty.proto', 'google/protobuf/timestamp.proto'))];
    expect(missingProtoImports(protos)).toEqual([]);
  });

  it('按 basename 匹配：import 带子目录路径也能命中同名文件', () => {
    const protos = [
      proto('root.proto', withImports('sub/common.proto')),
      proto('common.proto', 'syntax = "proto3";\n'),
    ];
    expect(missingProtoImports(protos)).toEqual([]);
  });

  it('多处引用同一缺失文件 → 去重', () => {
    const protos = [
      proto('a.proto', withImports('gone.proto')),
      proto('b.proto', withImports('gone.proto')),
    ];
    expect(missingProtoImports(protos)).toEqual(['gone.proto']);
  });

  it('多个不同缺失文件 → 排序返回', () => {
    const protos = [
      proto('a.proto', withImports('zzz.proto', 'aaa.proto')),
      proto('b.proto', withImports('mmm.proto')),
    ];
    expect(missingProtoImports(protos)).toEqual(['aaa.proto', 'mmm.proto', 'zzz.proto']);
  });

  it('import public / import weak 也识别', () => {
    const protos = [
      proto('a.proto', 'syntax = "proto3";\nimport public "shared.proto";\nimport weak "opt.proto";\n'),
    ];
    expect(missingProtoImports(protos)).toEqual(['opt.proto', 'shared.proto']);
  });

  it('被注释掉的 import 不误判（行注释 //）', () => {
    const protos = [
      proto('a.proto', 'syntax = "proto3";\n// import "commented.proto";\nmessage A {}\n'),
    ];
    expect(missingProtoImports(protos)).toEqual([]);
  });

  it('缩进的 import 也能识别', () => {
    const protos = [proto('a.proto', 'syntax = "proto3";\n    import "indented.proto";\n')];
    expect(missingProtoImports(protos)).toEqual(['indented.proto']);
  });
});

// ---------------- syncProtosToIdb（带 mock）----------------

/** 构造 fetch stub：按 URL 后缀分别返回 index.json 数组或文件文本（与 scriptSync 单测同款 plain object）。 */
function stubBaseline(index: string[] | null, contents: Record<string, string>): void {
  fetchMock.mockImplementation(async (url: string) => {
    const u = String(url);
    if (u.endsWith('/proto/index.json')) {
      if (index === null) return { ok: false, status: 502 };
      return { ok: true, status: 200, json: async () => index };
    }
    // 形如 .../proto/<name>
    const name = decodeURIComponent(u.split('/proto/').pop() ?? '');
    if (name in contents) {
      return { ok: true, status: 200, text: async () => contents[name] };
    }
    return { ok: false, status: 404 };
  });
}

describe('syncProtosToIdb', () => {
  it('基线索引不可用 → indexAvailable=false，不 fetch 内容、不写 IDB', async () => {
    stubBaseline(null, {});
    const res = await syncProtosToIdb();
    expect(res).toEqual({ added: [], skipped: [], missing: [], indexAvailable: false });
    // 只发了 index 请求，没有发内容请求
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(idb.size).toBe(0);
  });

  it('IDB 已有 → skipped，不覆盖本地内容', async () => {
    idb.set('common.proto', proto('common.proto', '// 本地编辑稿'));
    stubBaseline(['common.proto', 'enum.proto'], { 'common.proto': 'syntax="proto3";', 'enum.proto': 'syntax="proto3";' });
    const res = await syncProtosToIdb();
    expect(res.indexAvailable).toBe(true);
    expect(res.skipped).toEqual(['common.proto']);
    expect(res.added).toEqual(['enum.proto']);
    // 本地 common.proto 内容未被覆盖
    expect(idb.get('common.proto')?.content).toBe('// 本地编辑稿');
  });

  it('基线有、IDB 没有 → 拉回写入', async () => {
    stubBaseline(['custom_task.proto'], { 'custom_task.proto': 'syntax="proto3";\nmessage T{}' });
    const res = await syncProtosToIdb();
    expect(res.added).toEqual(['custom_task.proto']);
    expect(idb.get('custom_task.proto')?.content).toBe('syntax="proto3";\nmessage T{}');
  });

  it('内容拉取失败（索引有、内容 404）→ 收集到 missing，不写', async () => {
    stubBaseline(['present.proto', 'ghost.proto'], { 'present.proto': 'syntax="proto3";' });
    const res = await syncProtosToIdb();
    expect(res.added).toEqual(['present.proto']);
    expect(res.missing).toEqual(['ghost.proto']);
    expect(idb.has('ghost.proto')).toBe(false);
  });

  it('混合场景：部分已有、部分拉取、部分缺失', async () => {
    idb.set('a.proto', proto('a.proto', 'local'));
    stubBaseline(['a.proto', 'b.proto', 'c.proto'], { 'b.proto': 'b', /* c 故意不给内容 */ });
    const res = await syncProtosToIdb();
    expect(res.skipped).toEqual(['a.proto']);
    expect(res.added).toEqual(['b.proto']);
    expect(res.missing).toEqual(['c.proto']);
  });

  it('空索引（基线返回 []）→ indexAvailable=true，全空结果', async () => {
    stubBaseline([], {});
    const res = await syncProtosToIdb();
    expect(res).toEqual({ added: [], skipped: [], missing: [], indexAvailable: true });
  });
});
