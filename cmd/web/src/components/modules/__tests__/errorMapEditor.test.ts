import { describe, it, expect } from 'vitest';
import {
  parseErrorMap,
  validateErrorMap,
  serializeErrorMap,
  nextBusinessCode,
  validateErrorDraft,
  isDraftEngaged,
  matchesErrorQuery,
} from '../ErrorMapEditor';

describe('validateErrorMap', () => {
  it('拒绝 < 100 框架保留码', () => {
    const errs = validateErrorMap([{ code: 54, desc: '撞框架' }]);
    expect(errs.some((e) => /54.*< 100|保留/.test(e.message))).toBe(true);
  });
  it('拒绝重复码', () => {
    const errs = validateErrorMap([{ code: 1004, desc: 'a' }, { code: 1004, desc: 'b' }]);
    expect(errs.some((e) => /重复/.test(e.message))).toBe(true);
  });
  it('合法条目无错', () => {
    expect(validateErrorMap([{ code: 1004, desc: '队伍已满' }])).toHaveLength(0);
  });
});
describe('parseErrorMap / serializeErrorMap', () => {
  it('往返一致', () => {
    const json = '{"1004":"队伍已满","2002":"金币不足"}';
    const entries = parseErrorMap(json);
    expect(entries).toEqual([{ code: 1004, desc: '队伍已满' }, { code: 2002, desc: '金币不足' }]);
    expect(JSON.parse(serializeErrorMap(entries))).toEqual(JSON.parse(json));
  });

  it('非字符串描述不会让校验崩溃', () => {
    const entries = parseErrorMap('{"1004":1}');
    expect(() => validateErrorMap(entries)).not.toThrow();
    expect(validateErrorMap(entries).some((e) => /描述/.test(e.message))).toBe(true);
  });

  it('保留空描述条目以便编辑中往返（新增业务码后该行不丢）', () => {
    const out = serializeErrorMap([{ code: 1000, desc: '' }]);
    expect(JSON.parse(out)).toEqual({ 1000: '' });
    // 往返：解析回来仍是空描述条目，新增的行因此能在受控 UI 中存续
    expect(parseErrorMap(out)).toEqual([{ code: 1000, desc: '' }]);
  });

  it('丢弃无有效正整数码的条目（无法作为 JSON 键）', () => {
    const out = serializeErrorMap([
      { code: Number.NaN, desc: '坏码' },
      { code: 1004, desc: 'ok' },
    ]);
    expect(JSON.parse(out)).toEqual({ 1004: 'ok' });
  });
});

describe('nextBusinessCode', () => {
  it('从起点取首个未占用码', () => {
    expect(nextBusinessCode([])).toBe(1000);
    expect(nextBusinessCode([{ code: 1000, desc: 'a' }])).toBe(1001);
  });

  it('跳过已用码（含中间空洞也只递增查找）', () => {
    expect(
      nextBusinessCode([
        { code: 1000, desc: 'a' },
        { code: 1001, desc: 'b' },
      ]),
    ).toBe(1002);
  });
});

describe('validateErrorDraft', () => {
  const existing = [{ code: 1004, desc: '队伍已满' }];

  it('合法新码通过', () => {
    expect(validateErrorDraft(1000, '金币不足', null, existing)).toBeNull();
  });

  it('码 < 100 属框架保留段', () => {
    expect(validateErrorDraft(54, 'x', null, existing)).toMatch(/保留/);
  });

  it('与已有码重复拒绝（新增时）', () => {
    expect(validateErrorDraft(1004, 'x', null, existing)).toMatch(/已存在|重复/);
  });

  it('编辑自身改描述不判重复', () => {
    expect(validateErrorDraft(1004, '改描述', 1004, existing)).toBeNull();
  });

  it('空描述拒绝', () => {
    expect(validateErrorDraft(1001, '   ', null, [])).toMatch(/描述/);
  });

  it('非正整数码拒绝', () => {
    expect(validateErrorDraft(Number.NaN, 'x', null, [])).toMatch(/正整数/);
  });
});

describe('isDraftEngaged', () => {
  // 空载（刚进入错误码映射、尚未输入）不应立即报红；只有介入后才展示行内错误。
  it('初始空载未介入', () => {
    expect(isDraftEngaged(null, '', null)).toBe(false);
  });
  it('填了码即介入', () => {
    expect(isDraftEngaged(1000, '', null)).toBe(true);
  });
  it('编辑已有码即介入', () => {
    expect(isDraftEngaged(null, '', 1004)).toBe(true);
  });
  it('只填描述也介入', () => {
    expect(isDraftEngaged(null, '金币不足', null)).toBe(true);
  });
  it('纯空格描述不算介入（配合空载）', () => {
    expect(isDraftEngaged(null, '   ', null)).toBe(false);
  });
});

describe('matchesErrorQuery', () => {
  it('空串恒匹配', () => {
    expect(matchesErrorQuery(1000, '队伍已满', '')).toBe(true);
    expect(matchesErrorQuery(1000, '队伍已满', '   ')).toBe(true);
  });
  it('按码子串匹配', () => {
    expect(matchesErrorQuery(1000, 'x', '100')).toBe(true);
    expect(matchesErrorQuery(1000, 'x', '1000')).toBe(true);
    expect(matchesErrorQuery(2002, 'x', '1000')).toBe(false);
  });
  it('按描述匹配（大小写不敏感）', () => {
    expect(matchesErrorQuery(1000, '队伍已满', '队伍')).toBe(true);
    expect(matchesErrorQuery(1000, 'Gold Not Enough', 'gold')).toBe(true);
  });
  it('码与描述都不匹配', () => {
    expect(matchesErrorQuery(1000, '队伍已满', 'xyz')).toBe(false);
  });
});
