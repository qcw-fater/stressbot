import { describe, expect, it } from 'vitest';
import {
  buildRouteTemplateFields,
  extractRouteTemplatePlaceholders,
  formatRouteFieldDraft,
  parseRouteScalarDraft,
  updateRouteTemplateField,
} from './routeFormModel';

describe('extractRouteTemplatePlaceholders', () => {
  it('按首次出现顺序提取并去重', () => {
    expect(extractRouteTemplatePlaceholders('{alpha}:{beta}:{alpha}')).toEqual(['alpha', 'beta']);
  });

  it('无占位模板返回空列表', () => {
    expect(extractRouteTemplatePlaceholders('static-route')).toEqual([]);
  });
});

describe('route scalar draft', () => {
  it('格式化已有字段值', () => {
    expect(formatRouteFieldDraft(42)).toBe('42');
    expect(formatRouteFieldDraft('ready')).toBe('ready');
    expect(formatRouteFieldDraft(undefined)).toBe('');
  });

  it('解析数字、布尔和字符串标量', () => {
    expect(parseRouteScalarDraft('42')).toEqual({ ok: true, value: 42 });
    expect(parseRouteScalarDraft('true')).toEqual({ ok: true, value: true });
    expect(parseRouteScalarDraft('"ready"')).toEqual({ ok: true, value: 'ready' });
    expect(parseRouteScalarDraft('ready')).toEqual({ ok: true, value: 'ready' });
  });

  it('拒绝对象和数组', () => {
    expect(parseRouteScalarDraft('{"alpha":1}')).toMatchObject({ ok: false });
    expect(parseRouteScalarDraft('[1,2]')).toMatchObject({ ok: false });
  });
});

describe('template route updates', () => {
  it('生成字段模型并标记缺失字段', () => {
    expect(buildRouteTemplateFields('{alpha}:{beta}', { alpha: 1 })).toMatchObject([
      { name: 'alpha', draft: '1', missing: false },
      { name: 'beta', draft: '', missing: true },
    ]);
  });

  it('更新指定字段并保留额外 key', () => {
    const result = updateRouteTemplateField({ alpha: 1, extra: 'keep' }, 'beta', '2');
    expect(result).toEqual({ ok: true, route: { alpha: 1, beta: 2, extra: 'keep' } });
  });

  it('非对象 route 首次编辑后生成对象', () => {
    const result = updateRouteTemplateField('legacy', 'alpha', 'ready');
    expect(result).toEqual({ ok: true, route: { alpha: 'ready' } });
  });
});
