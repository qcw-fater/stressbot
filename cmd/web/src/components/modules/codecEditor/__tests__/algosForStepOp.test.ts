/**
 * T3 Batch-3 任务 A — algosForStepOp 映射单测。
 *
 * 关键 gotcha：PipelineStep.op ∈ {compress,encrypt,checksum,hash}，
 *              AlgoMeta.op     ∈ {cipher,compress,checksum,hash}。
 * encrypt↔cipher 是唯一映射差异，其余三个同名。
 */

import { describe, expect, it } from 'vitest';
import { algosForStepOp } from '../algosForStepOp';
import type { AlgoMeta } from '@/types/codec';

const ALGOS: AlgoMeta[] = [
  { name: 'aes', op: 'cipher' },
  { name: 'rc4', op: 'cipher' },
  { name: 'gzip', op: 'compress' },
  { name: 'xor8', op: 'checksum' },
  { name: 'crc32', op: 'checksum' },
  { name: 'md5', op: 'hash' },
];

describe('algosForStepOp', () => {
  it("step.op='encrypt' 映射到 AlgoMeta.op='cipher'", () => {
    const r = algosForStepOp(ALGOS, 'encrypt');
    expect(r.map((a) => a.name)).toEqual(['aes', 'rc4']);
    expect(r.every((a) => a.op === 'cipher')).toBe(true);
  });

  it("step.op='compress' 同名映射", () => {
    expect(algosForStepOp(ALGOS, 'compress').map((a) => a.name)).toEqual(['gzip']);
  });

  it("step.op='checksum' 同名映射", () => {
    expect(algosForStepOp(ALGOS, 'checksum').map((a) => a.name)).toEqual(['xor8', 'crc32']);
  });

  it("step.op='hash' 同名映射", () => {
    expect(algosForStepOp(ALGOS, 'hash').map((a) => a.name)).toEqual(['md5']);
  });

  it('未知 op 返回空（不抛、不兜底）', () => {
    expect(algosForStepOp(ALGOS, 'whatever')).toEqual([]);
  });

  it('空 algo 清单返回空', () => {
    expect(algosForStepOp([], 'encrypt')).toEqual([]);
  });

  it('保持清单原顺序（不重排）', () => {
    const r = algosForStepOp(ALGOS, 'checksum');
    expect(r.map((a) => a.name)).toEqual(['xor8', 'crc32']);
  });
});
