/**
 * T3 Batch-2 任务 A — codecEdit 同步 helper 单测。
 *
 * 核心断言：raw-object 无损 round-trip（保留未知键 + 原始键序），结构化编辑只动预期部分。
 * 镜像 brief §3.1 规格。
 */

import { describe, expect, it } from 'vitest';
import {
  parseCodecForEdit,
  serializeCodec,
  addHeaderField,
  updateHeaderField,
  removeHeaderField,
  moveHeaderField,
  setCodecScalar,
  addPipelineStep,
  updatePipelineStep,
  removePipelineStep,
  movePipelineStep,
  setRouteKeyTemplate,
} from '../codecEdit';
import type { Field } from '@/types/codec';

/** 含未知键 + 非默认键序的样本，用于验证 lossless round-trip。 */
const SAMPLE_WITH_UNKNOWN = `{
  "version": 1,
  "endianDefault": "le",
  "customMarker": "keep-me",
  "frame": { "headerSize": 8, "trailerSize": 0, "lengthIncludesHeader": false, "lengthIncludesTrailer": false },
  "header": [
    { "name": "bodyLen", "offset": 0, "size": 4, "type": "u32", "endian": "le", "role": "length" },
    { "name": "cmd",     "offset": 4, "size": 1, "type": "u8",  "role": "route" }
  ],
  "routeKeyTemplate": "{cmd}",
  "pipeline": [],
  " trailingComment ": "trailing-unknown"
}
`;

describe('parseCodecForEdit — 解析', () => {
  it('合法 JSON → raw + schema 都有，error 为 null', () => {
    const r = parseCodecForEdit(SAMPLE_WITH_UNKNOWN);
    expect(r.error).toBeNull();
    expect(r.raw).not.toBeNull();
    expect(r.schema).not.toBeNull();
    expect(r.schema!.version).toBe(1);
    expect(r.schema!.frame.headerSize).toBe(8);
  });

  it('保留未知键（customMarker / trailingComment）', () => {
    const r = parseCodecForEdit(SAMPLE_WITH_UNKNOWN);
    expect(r.raw!['customMarker']).toBe('keep-me');
    // JSON.parse 保留字符串字面键（含空格），序列化时原样写回。
    expect(r.raw![' trailingComment ']).toBe('trailing-unknown');
  });

  it('非法 JSON → raw/schema 为 null + 中文 error', () => {
    const r = parseCodecForEdit('{ not valid json');
    expect(r.raw).toBeNull();
    expect(r.schema).toBeNull();
    expect(r.error).toContain('合法 JSON');
  });

  it('解析为数组（非对象）→ schema 视为宽松（frame 缺省），不抛错', () => {
    // parseCodecForEdit 只关心「能否 JSON.parse」；结构合法性交给 validateCodecSchema。
    const r = parseCodecForEdit('[]');
    expect(r.raw).not.toBeNull();
    expect(Array.isArray(r.raw)).toBe(true);
    expect(r.error).toBeNull();
  });
});

describe('serializeCodec — 确定性 + lossless', () => {
  it('parse → serialize 字节级 round-trip（保留未知键与原序）', () => {
    const { raw } = parseCodecForEdit(SAMPLE_WITH_UNKNOWN);
    expect(serializeCodec(raw!)).toBe(JSON.stringify(JSON.parse(SAMPLE_WITH_UNKNOWN), null, 2));
  });

  it('保留原始键序（version 在 endianDefault 之前，customMarker 在 frame 之前）', () => {
    const { raw } = parseCodecForEdit(SAMPLE_WITH_UNKNOWN);
    const keys = Object.keys(raw!);
    expect(keys.indexOf('version')).toBeLessThan(keys.indexOf('endianDefault'));
    expect(keys.indexOf('endianDefault')).toBeLessThan(keys.indexOf('customMarker'));
    expect(keys.indexOf('customMarker')).toBeLessThan(keys.indexOf('frame'));
  });

  it('2 空格缩进、确定性输出', () => {
    const { raw } = parseCodecForEdit('{"a":1,"b":[1,2]}');
    expect(serializeCodec(raw!)).toBe('{\n  "a": 1,\n  "b": [\n    1,\n    2\n  ]\n}');
  });
});

describe('header 字段增删改 — 不 mutate 入参，只动预期部分', () => {
  const baseContent = `{
  "version": 1,
  "endianDefault": "le",
  "frame": { "headerSize": 8, "trailerSize": 0, "lengthIncludesHeader": false, "lengthIncludesTrailer": false },
  "header": [
    { "name": "bodyLen", "offset": 0, "size": 4, "type": "u32", "endian": "le", "role": "length" },
    { "name": "cmd",     "offset": 4, "size": 1, "type": "u8",  "role": "route" }
  ],
  "routeKeyTemplate": "{cmd}",
  "pipeline": []
}
`;

  it('addHeaderField：追加到末尾，不 mutate 入参 raw', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const snapshot = JSON.parse(JSON.stringify(raw));
    const newField: Field = { name: 'act', offset: 5, size: 1, type: 'u8', role: 'route' };
    const next = addHeaderField(raw!, newField);
    // 入参 raw 未被改
    expect(raw!).toEqual(snapshot);
    const parsed = JSON.parse(next);
    expect(parsed.header).toHaveLength(3);
    expect(parsed.header[2]).toEqual(newField);
  });

  it('updateHeaderField：局部 patch，保留该字段其他键', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const next = updateHeaderField(raw!, 1, { size: 2, type: 'u16' });
    const parsed = JSON.parse(next);
    expect(parsed.header[1].size).toBe(2);
    expect(parsed.header[1].type).toBe('u16');
    // 未改的键保留
    expect(parsed.header[1].name).toBe('cmd');
    expect(parsed.header[1].role).toBe('route');
    expect(parsed.header[0]).toEqual(JSON.parse(baseContent).header[0]);
  });

  it('removeHeaderField：删除指定 index，其余顺序不变', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const next = removeHeaderField(raw!, 0);
    const parsed = JSON.parse(next);
    expect(parsed.header).toHaveLength(1);
    expect(parsed.header[0].name).toBe('cmd');
  });

  it('moveHeaderField：上移/下移交换相邻，越界保持原序', () => {
    const { raw } = parseCodecForEdit(baseContent);
    // 下移 index 0 → 与 1 交换
    const down = moveHeaderField(raw!, 0, 1);
    const downParsed = JSON.parse(down);
    expect(downParsed.header[0].name).toBe('cmd');
    expect(downParsed.header[1].name).toBe('bodyLen');
    // 上移 index 1 → 与 0 交换（回到原序）
    const up = moveHeaderField(downParsed, 1, -1);
    const upParsed = JSON.parse(up);
    expect(upParsed.header[0].name).toBe('bodyLen');
    expect(upParsed.header[1].name).toBe('cmd');
    // 越界：index 0 上移保持原序
    const oob = moveHeaderField(raw!, 0, -1);
    const oobParsed = JSON.parse(oob);
    expect(oobParsed.header.map((h: Field) => h.name)).toEqual(['bodyLen', 'cmd']);
  });

  it('header 不是数组时 add/remove/update 安全降级（返回原 content，不抛错）', () => {
    const content = '{"version":1,"header":"oops"}';
    const { raw } = parseCodecForEdit(content);
    expect(() => addHeaderField(raw!, { name: 'x', offset: 0, size: 1, type: 'u8', role: 'reserved' })).not.toThrow();
    expect(() => updateHeaderField(raw!, 0, { size: 2 })).not.toThrow();
    expect(() => removeHeaderField(raw!, 0)).not.toThrow();
    expect(() => moveHeaderField(raw!, 0, 1)).not.toThrow();
  });
});

describe('setCodecScalar — 标量编辑', () => {
  const baseContent = `{
  "version": 1,
  "endianDefault": "le",
  "frame": { "headerSize": 8, "trailerSize": 0, "lengthIncludesHeader": false, "lengthIncludesTrailer": false },
  "header": [],
  "routeKeyTemplate": "",
  "pipeline": []
}
`;

  it('version / endianDefault 顶层标量', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const next = setCodecScalar(raw!, 'version', 2);
    expect(JSON.parse(next).version).toBe(2);
    const nextEndian = setCodecScalar(JSON.parse(next), 'endianDefault', 'be');
    expect(JSON.parse(nextEndian).endianDefault).toBe('be');
  });

  it('frame.headerSize / trailerSize / lengthIncludes* 嵌套路径', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const next = setCodecScalar(raw!, 'frame.headerSize', 16);
    const parsed = JSON.parse(next);
    expect(parsed.frame.headerSize).toBe(16);
    // 其他 frame 键保留
    expect(parsed.frame.trailerSize).toBe(0);

    const t = setCodecScalar(JSON.parse(next), 'frame.trailerSize', 4);
    expect(JSON.parse(t).frame.trailerSize).toBe(4);

    const lh = setCodecScalar(JSON.parse(t), 'frame.lengthIncludesHeader', true);
    expect(JSON.parse(lh).frame.lengthIncludesHeader).toBe(true);

    const lt = setCodecScalar(JSON.parse(lh), 'frame.lengthIncludesTrailer', true);
    expect(JSON.parse(lt).frame.lengthIncludesTrailer).toBe(true);
  });

  it('不 mutate 入参 raw', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const snapshot = JSON.parse(JSON.stringify(raw));
    setCodecScalar(raw!, 'frame.headerSize', 32);
    expect(raw!).toEqual(snapshot);
  });
});

describe('pipeline 步骤增删改/移动 — raw 无损 + 不 mutate', () => {
  const baseContent = `{
  "version": 1,
  "endianDefault": "le",
  "customMarker": "keep-me",
  "frame": { "headerSize": 8, "trailerSize": 0, "lengthIncludesHeader": false, "lengthIncludesTrailer": false },
  "header": [
    { "name": "bodyLen", "offset": 0, "size": 4, "type": "u32", "endian": "le", "role": "length" },
    { "name": "cmd",     "offset": 4, "size": 1, "type": "u8",  "role": "route" }
  ],
  "routeKeyTemplate": "{cmd}",
  "pipeline": [
    { "op": "compress", "name": "zip", "algo": "zlib" }
  ]
}
`;

  it('addPipelineStep：追加默认 {op:compress,name:"",algo:""}，不 mutate 入参，保留未知键', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const snapshot = JSON.parse(JSON.stringify(raw));
    const next = addPipelineStep(raw!);
    expect(raw!).toEqual(snapshot); // 入参未改
    const parsed = JSON.parse(next);
    expect(parsed.pipeline).toHaveLength(2);
    expect(parsed.pipeline[1]).toEqual({ op: 'compress', name: '', algo: '' });
    // 未知键保留
    expect(parsed.customMarker).toBe('keep-me');
  });

  it('addPipelineStep：支持传入 partial（如 encrypt + offset + produces）', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const next = addPipelineStep(raw!, {
      op: 'encrypt',
      name: 'enc',
      algo: 'xxtea',
      keyLen: 16,
      offset: { encode: 11, decode: 0 },
      produces: [{ name: 'bcc', algo: 'xor8', region: 'ciphered' }],
    });
    const parsed = JSON.parse(next);
    expect(parsed.pipeline[1]).toMatchObject({
      op: 'encrypt',
      name: 'enc',
      offset: { encode: 11, decode: 0 },
      keyLen: 16,
    });
    expect(parsed.pipeline[1].produces[0].region).toBe('ciphered');
  });

  it('updatePipelineStep：局部 patch 合并，保留该步其他键', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const next = updatePipelineStep(raw!, 0, { algo: 'gzip', onError: 'keep' });
    const parsed = JSON.parse(next);
    expect(parsed.pipeline[0].algo).toBe('gzip');
    expect(parsed.pipeline[0].onError).toBe('keep');
    // 未改的 op/name 保留
    expect(parsed.pipeline[0].op).toBe('compress');
    expect(parsed.pipeline[0].name).toBe('zip');
  });

  it('updatePipelineStep：越界 index 安全降级（返回原 content 序列化）', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const next = updatePipelineStep(raw!, 99, { algo: 'x' });
    // 内容不变（只重新序列化）
    expect(JSON.parse(next)).toEqual(JSON.parse(baseContent));
  });

  it('removePipelineStep：删除指定 index', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const added = addPipelineStep(raw!, { op: 'encrypt', name: 'enc', algo: 'xxtea' });
    const removed = removePipelineStep(JSON.parse(added), 0);
    const parsed = JSON.parse(removed);
    expect(parsed.pipeline).toHaveLength(1);
    expect(parsed.pipeline[0].name).toBe('enc');
  });

  it('movePipelineStep：上移/下移交换，越界保持原序', () => {
    const { raw } = parseCodecForEdit(baseContent);
    let content = addPipelineStep(raw!, { op: 'encrypt', name: 'enc', algo: 'xxtea' });
    content = addPipelineStep(JSON.parse(content), { op: 'checksum', name: 'cksum', algo: 'crc32' });
    // 顺序：zip, enc, cksum
    // 下移 index 0 → enc, zip, cksum
    const down = movePipelineStep(JSON.parse(content), 0, 1);
    const downParsed = JSON.parse(down);
    expect(downParsed.pipeline.map((s: { name: string }) => s.name)).toEqual(['enc', 'zip', 'cksum']);
    // 上移 index 2 → enc, cksum, zip
    const up = movePipelineStep(downParsed, 2, -1);
    expect(JSON.parse(up).pipeline.map((s: { name: string }) => s.name)).toEqual(['enc', 'cksum', 'zip']);
    // 越界：index 0 上移保持原序
    const oob = movePipelineStep(JSON.parse(content), 0, -1);
    expect(JSON.parse(oob).pipeline.map((s: { name: string }) => s.name)).toEqual(['zip', 'enc', 'cksum']);
  });

  it('pipeline 不是数组时 add 安全降级（创建数组）', () => {
    const content = '{"version":1,"pipeline":"oops"}';
    const { raw } = parseCodecForEdit(content);
    expect(() => addPipelineStep(raw!)).not.toThrow();
    const next = addPipelineStep(raw!);
    const parsed = JSON.parse(next);
    expect(Array.isArray(parsed.pipeline)).toBe(true);
    expect(parsed.pipeline).toHaveLength(1);
  });

  it('serialize 稳定：未知键/原序不丢', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const next = addPipelineStep(raw!, { op: 'hash', name: 'h', algo: 'md5' });
    const reparsed = parseCodecForEdit(next);
    expect(reparsed.raw!['customMarker']).toBe('keep-me');
    // 键序：version < endianDefault < customMarker < frame（原序保留）
    const keys = Object.keys(reparsed.raw!);
    expect(keys.indexOf('version')).toBeLessThan(keys.indexOf('customMarker'));
    expect(keys.indexOf('customMarker')).toBeLessThan(keys.indexOf('frame'));
  });
});

describe('setRouteKeyTemplate — raw 无损 + 不 mutate', () => {
  const baseContent = `{
  "version": 1,
  "endianDefault": "le",
  "customMarker": "keep-me",
  "frame": { "headerSize": 8, "trailerSize": 0, "lengthIncludesHeader": false, "lengthIncludesTrailer": false },
  "header": [
    { "name": "cmd", "offset": 0, "size": 1, "type": "u8", "role": "route" },
    { "name": "act", "offset": 1, "size": 1, "type": "u8", "role": "route" }
  ],
  "routeKeyTemplate": "{cmd}",
  "pipeline": []
}
`;

  it('设置 routeKeyTemplate，保留未知键 + 不 mutate 入参', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const snapshot = JSON.parse(JSON.stringify(raw));
    const next = setRouteKeyTemplate(raw!, '{cmd}:{act}');
    expect(raw!).toEqual(snapshot);
    const parsed = JSON.parse(next);
    expect(parsed.routeKeyTemplate).toBe('{cmd}:{act}');
    expect(parsed.customMarker).toBe('keep-me');
  });

  it('空串也可设置（最终校验交给 validateCodecSchema）', () => {
    const { raw } = parseCodecForEdit(baseContent);
    const next = setRouteKeyTemplate(raw!, '');
    expect(JSON.parse(next).routeKeyTemplate).toBe('');
  });
});
