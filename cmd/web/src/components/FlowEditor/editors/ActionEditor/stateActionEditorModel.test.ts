import { describe, expect, it } from 'vitest';
import { ALL_BINDING_TYPES, type FieldBind } from '@/types/action';
import {
  SET_STATE_TYPE_GROUPS,
  bindingAdvancedCount,
  bindingValueSummary,
  changeBindingType,
  moveBinding,
} from './stateActionEditorModel';

describe('state action editor model', () => {
  it('取值方式完整覆盖且不重复 17 种 binding type', () => {
    const flattened = SET_STATE_TYPE_GROUPS.flatMap((group) => group.types);
    expect(new Set(flattened).size).toBe(ALL_BINDING_TYPES.length);
    expect([...flattened].sort()).toEqual([...ALL_BINDING_TYPES].sort());
  });

  it('摘要显示固定值和状态来源', () => {
    expect(bindingValueSummary({ type: 'fixed', value: true })).toBe('true');
    expect(bindingValueSummary({ type: 'state', source: 'matchInfo', path: 'id' })).toBe('matchInfo.id');
  });

  it('切换类型删除旧类型参数但保留目标和通用高级配置', () => {
    const before: FieldBind = {
      field: 'battleId', type: 'stateRandomN', source: 'matches', path: 'id', count: 2,
      required: true, condition: 'state:ready', storeAs: 'picked',
    };
    expect(changeBindingType(before, 'fixed')).toEqual({
      field: 'battleId', type: 'fixed', required: true, condition: 'state:ready', storeAs: 'picked',
    });
  });

  it('高级配置数量包含通用字段和类型高级字段', () => {
    expect(bindingAdvancedCount({
      type: 'stateRandom', source: 'items', path: 'id', filters: [{ op: 'eq', value: 1 }], optional: true,
    })).toBe(3);
  });

  it('移动条目保持不可变并守住边界', () => {
    const list: FieldBind[] = [{ type: 'fixed', field: 'a' }, { type: 'fixed', field: 'b' }];
    expect(moveBinding(list, 0, 1).map((b) => b.field)).toEqual(['b', 'a']);
    expect(moveBinding(list, 0, -1)).toBe(list);
  });
});
