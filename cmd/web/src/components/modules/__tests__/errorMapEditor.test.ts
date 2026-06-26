import { describe, it, expect } from 'vitest';
import { parseErrorMap, validateErrorMap, serializeErrorMap } from '../ErrorMapEditor';

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
});
