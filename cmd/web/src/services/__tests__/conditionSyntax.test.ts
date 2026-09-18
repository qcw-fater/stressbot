/**
 * conditionSyntax 的判定必须与 Go 端一致：能过后端 compileCondition 的都放行，
 * 后端会拒的都报出来。
 *
 * 下面两张用例表直接抄自 `flow/condition_parser_test.go` 的 TestCompileConditionSyntax，
 * 抄而不是另写一套，是为了让「哪些表达式合法」只有一份权威清单——文法改动时两边
 * 用例表对不上，就是该同步前端镜像的信号。
 */

import { describe, expect, it } from 'vitest';
import { checkConditionSyntax } from '../conditionSyntax';

/** 后端接受的表达式（以 state: 前缀提交，此处按前端调用形态补上前缀）。 */
const VALID = [
  '', // 空表达式跳过
  '  ', // 纯空白跳过
  'hp > 0', // 基本比较
  'hp > 0 && lvl >= 10', // 逻辑与
  '!(hp > 0) || alive', // 一元 + 括号 + 逻辑或
  'index % 2 == 0', // 取模
  '(a > 1) && (b < 2 || c)', // 嵌套括号
  'missingKey > 0', // 缺失 state 路径仍是合法语法
];

/** 后端拒绝的表达式（加载期 fail-closed）。 */
const INVALID = [
  '(', // 未闭合括号
  '((', // 多层未闭合
  'hp >', // 比较缺右操作数
  'hp > (', // 右操作数为未闭合括号
  '!', // 一元缺操作数
  '&&', // 二元缺两侧
  'hp &&', // 缺右操作数
  '(hp > 0', // 缺右括号
  'hp > 0)', // 多余右括号
  '1 +', // 算术缺右操作数
  '- ', // 负号缺操作数
  ') (', // 顺序错乱
];

describe('checkConditionSyntax', () => {
  it.each(VALID)('放行后端接受的 state:%s', (expr) => {
    expect(checkConditionSyntax(`state:${expr}`)).toBeNull();
  });

  it.each(INVALID)('拦下后端拒绝的 state:%s', (expr) => {
    expect(checkConditionSyntax(`state:${expr}`)).not.toBeNull();
  });

  it('放行空条件与 lua: 条件', () => {
    // 空条件后端编译为恒真；lua: 的脚本内容不归语法检查管（脚本是否存在由资源同步链路负责）
    expect(checkConditionSyntax(undefined)).toBeNull();
    expect(checkConditionSyntax('   ')).toBeNull();
    expect(checkConditionSyntax('lua:check.lua')).toBeNull();
    expect(checkConditionSyntax('state:')).toBeNull();
  });

  it('缺前缀报错——后端不会按 state 兜底', () => {
    const issue = checkConditionSyntax('hp > 0');
    expect(issue?.message).toContain('缺少前缀');
    expect(issue?.pos).toBe(0);
  });

  it('把单字符 = / | / & 认成双字符运算符的笔误', () => {
    expect(checkConditionSyntax('state:hp = 1')?.message).toContain('是否想用 ==');
    expect(checkConditionSyntax('state:a | b')?.message).toContain('是否想用 ||');
    expect(checkConditionSyntax('state:a & b')?.message).toContain('是否想用 &&');
  });

  it('报出词法层错误：未闭合字符串、非法字符、数组下标', () => {
    expect(checkConditionSyntax('state:name == "abc')?.message).toContain('未闭合的字符串字面量');
    expect(checkConditionSyntax('state:hp # 1')?.message).toContain('非法字符');
    expect(checkConditionSyntax('state:list[a] > 0')?.message).toContain('数组下标必须为数字');
    expect(checkConditionSyntax('state:list[0 > 0')?.message).toContain('数组下标缺少右括号');
  });

  it('位置相对表达式正文，不含 state: 前缀', () => {
    // "hp # 1" 里 # 在正文偏移 3；带上前缀算的话会是 9，提示就对不上后端日志了
    expect(checkConditionSyntax('state:hp # 1')?.pos).toBe(3);
  });

  it('接受点分路径、数组下标与字符串字面量', () => {
    expect(checkConditionSyntax('state:player.bag[0].count >= 3')).toBeNull();
    expect(checkConditionSyntax('state:job == "member" && level.cur > 1.5')).toBeNull();
  });

  it('比较运算不可串联', () => {
    // 后端 parseComparison 只吃一次比较，剩下的落到「存在多余 token」
    expect(checkConditionSyntax('state:a == b == c')?.message).toContain('多余 token');
  });
});
