/**
 * 校验问题的类型、构造与汇总。
 *
 * 独立成文件是为了让 refsCheck（流程级规则）、actionRules（动作级规则）、
 * bindingRules（字段绑定级规则）都能引用同一套定义，而不必互相 import 造成环。
 */

import { checkConditionSyntax } from '@/services/conditionSyntax';

export type Severity = 'error' | 'warning' | 'info';

export interface ValidationIssue {
  severity: Severity;
  code: string;
  message: string;
  /** 用于 UI 跳转：node id / action 名 / listen 名 */
  location?: { kind: 'node' | 'action' | 'listen'; id: string };
}

export interface ValidationReport {
  errors: ValidationIssue[];
  warnings: ValidationIssue[];
  infos: ValidationIssue[];
  total: number;
}

/**
 * 条件表达式语法检查，语法不合法时产出一条 error。
 *
 * 定 error 而不是 warning：后端 `PrepareTaskFlow` 编译条件失败会让**整份流程**加载不了，
 * 任务根本起不来，不存在「带着这个问题也能跑」的情形。
 *
 * 检查面必须与 Go 端的编译点一一对应（`flow/prepare.go`：节点 condition / breakCondition /
 * cases[].condition / 各 FieldBind.condition）。少查一处，用户就只能在启动任务时拿到一条
 * 没有画布定位的加载失败。
 */
export function conditionIssue(
  where: string,
  raw: string | undefined,
  location: ValidationIssue['location'],
): ValidationIssue | null {
  const syntax = checkConditionSyntax(raw);
  if (!syntax) return null;
  return {
    severity: 'error',
    code: 'CONDITION_SYNTAX',
    message: `${where} 条件表达式语法错误：${syntax.message}`,
    location,
  };
}

/** 按严重级把问题列表汇总成报告。 */
export function categorize(issues: ValidationIssue[]): ValidationReport {
  const errors = issues.filter((i) => i.severity === 'error');
  const warnings = issues.filter((i) => i.severity === 'warning');
  const infos = issues.filter((i) => i.severity === 'info');
  return { errors, warnings, infos, total: issues.length };
}

