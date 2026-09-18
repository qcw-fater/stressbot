/**
 * 条件表达式语法检查（纯函数，无 IO / 无 React 依赖）。
 *
 * 这是 Go 端 `flow/condition_tokenizer.go` + `flow/condition_compile.go` 的镜像：同一套词法
 * 与文法，同一套报错措辞。之所以敢在前端镜像一份，是因为后端**加载期只做语法检查**——
 * 类型（字符串比数字之类）在运行期按实际 state 值判定，编译期不参与，数字字面量溢出也只
 * 编译成运行期错误节点。也就是说「能不能通过 PrepareTaskFlow」完全由本文件这套文法决定，
 * 前端可以给出与后端一致的判定，而不是一个近似子集。
 *
 * 不做这层检查的代价：`state:hp >` 这种笔误在编辑器里一路绿灯，保存成功，直到启动任务时
 * 后端 PrepareTaskFlow 整份流程加载失败——报错里只有节点 id，画布上没有任何标记。
 *
 * 改 Go 那两个文件的文法时必须同步改这里（以及 conditionSyntax.test.ts 的用例表）。
 */

export interface ConditionSyntaxIssue {
  /** 报错文案，措辞与后端一致，便于和后端日志对照 */
  message: string;
  /**
   * 出错位置。与后端一样，偏移相对于 `state:` 前缀之后的表达式正文（前缀本身缺失时为 0）。
   * 后端按字节计、这里按 UTF-16 码元计：正文里只有字符串字面量可能含非 ASCII 字符，
   * 两侧的数字会在那种情况下有出入，仅影响提示里的位置数值。
   */
  pos: number;
}

const PREFIX_STATE = 'state:';
const PREFIX_LUA = 'lua:';

/**
 * 检查一条条件表达式能否通过后端加载期编译。返回 null 表示通过。
 *
 * 与后端 `compileCondition` 一致的几处放行：
 *   - 空串放行（后端编译为恒真，节点是否允许空条件由各自的必填校验负责）；
 *   - `lua:` 开头一律放行（脚本内容不在这里判定，脚本是否存在由资源同步链路负责）；
 *   - `state:` 后为空放行（同样编译为恒真）。
 */
export function checkConditionSyntax(raw: string | undefined): ConditionSyntaxIssue | null {
  const source = (raw ?? '').trim();
  if (source === '') return null;
  if (source.startsWith(PREFIX_LUA)) return null;
  if (!source.startsWith(PREFIX_STATE)) {
    return {
      // 缺前缀后端是硬报错而非默认按 state 处理：运行期无从判断作者本意是状态比较还是脚本。
      message: `条件表达式 ${quote(source)} 缺少前缀：必须以 "state:"（内置比较）或 "lua:"（脚本求值）开头`,
      pos: 0,
    };
  }

  const input = source.slice(PREFIX_STATE.length).trim();
  if (input === '') return null;

  const lexed = tokenize(input);
  if ('issue' in lexed) {
    return { message: `词法错误: ${lexed.issue.message}`, pos: lexed.issue.pos };
  }

  const parser: Parser = { toks: lexed.toks, i: 0 };
  const issue = parseOr(parser);
  if (issue) return issue;
  const rest = peek(parser);
  if (rest.kind !== 'eof') {
    return { message: `存在多余 token ${quote(rest.lit)}（位置 ${rest.pos}）`, pos: rest.pos };
  }
  return null;
}

// ── 词法 ────────────────────────────────────────────────────

type TokKind = 'number' | 'string' | 'path' | 'op' | 'lparen' | 'rparen' | 'eof';

interface Tok {
  kind: TokKind;
  /** number/path 为原始文本，string 为去引号内容，op 为运算符文本 */
  lit: string;
  pos: number;
}

function tokenize(input: string): { toks: Tok[] } | { issue: ConditionSyntaxIssue } {
  const toks: Tok[] = [];
  const n = input.length;
  let i = 0;
  while (i < n) {
    const c = input[i];
    if (c === ' ' || c === '\t' || c === '\n' || c === '\r') {
      i++;
      continue;
    }
    const start = i;

    if (isDigit(c)) {
      i++;
      while (i < n && isDigit(input[i])) i++;
      // 小数点后必须跟数字，否则点不属于这个数字（留给后续 token，通常会报意外 token）
      if (i < n && input[i] === '.' && i + 1 < n && isDigit(input[i + 1])) {
        i++;
        while (i < n && isDigit(input[i])) i++;
      }
      toks.push({ kind: 'number', lit: input.slice(start, i), pos: start });
      continue;
    }

    if (c === '"') {
      i++;
      while (i < n && input[i] !== '"') i++;
      if (i >= n) {
        return { issue: { message: `未闭合的字符串字面量（位置 ${start}）`, pos: start } };
      }
      const lit = input.slice(start + 1, i);
      i++;
      toks.push({ kind: 'string', lit, pos: start });
      continue;
    }

    if (isIdentStart(c)) {
      i++;
      while (i < n && isIdentCont(input[i])) i++;
      // 续段：.seg 或 [N]
      for (;;) {
        if (input[i] === '.' && i + 1 < n && isIdentStart(input[i + 1])) {
          i++;
          while (i < n && isIdentCont(input[i])) i++;
          continue;
        }
        if (input[i] === '[') {
          let j = i + 1;
          while (j < n && isDigit(input[j])) j++;
          if (j === i + 1) {
            return { issue: { message: `数组下标必须为数字（位置 ${i}）`, pos: i } };
          }
          if (j >= n || input[j] !== ']') {
            return { issue: { message: `数组下标缺少右括号 ]（位置 ${i}）`, pos: i } };
          }
          i = j + 1;
          continue;
        }
        break;
      }
      toks.push({ kind: 'path', lit: input.slice(start, i), pos: start });
      continue;
    }

    if (c === '(') {
      toks.push({ kind: 'lparen', lit: '(', pos: start });
      i++;
      continue;
    }
    if (c === ')') {
      toks.push({ kind: 'rparen', lit: ')', pos: start });
      i++;
      continue;
    }

    if (c === '|' || c === '&' || c === '=') {
      // 单字符 = / | / & 一律当笔误报错，没有单字符形态的运算符
      if (i + 1 < n && input[i + 1] === c) {
        toks.push({ kind: 'op', lit: input.slice(i, i + 2), pos: start });
        i += 2;
        continue;
      }
      return {
        issue: {
          message: `非法字符 ${charQuote(c)}（位置 ${start}，是否想用 ${c}${c}）`,
          pos: start,
        },
      };
    }

    if (c === '!' || c === '>' || c === '<') {
      if (i + 1 < n && input[i + 1] === '=') {
        toks.push({ kind: 'op', lit: c + '=', pos: start });
        i += 2;
      } else {
        toks.push({ kind: 'op', lit: c, pos: start });
        i++;
      }
      continue;
    }

    if (c === '+' || c === '-' || c === '*' || c === '/' || c === '%') {
      toks.push({ kind: 'op', lit: c, pos: start });
      i++;
      continue;
    }

    return { issue: { message: `非法字符 ${charQuote(c)}（位置 ${start}）`, pos: start } };
  }
  toks.push({ kind: 'eof', lit: '', pos: n });
  return { toks };
}

function isDigit(c: string): boolean {
  return c >= '0' && c <= '9';
}

function isIdentStart(c: string): boolean {
  return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c === '_';
}

function isIdentCont(c: string): boolean {
  return isIdentStart(c) || isDigit(c);
}

// ── 文法 ────────────────────────────────────────────────────
//
// or   := and ('||' and)*
// and  := unary ('&&' unary)*
// unary:= '!' unary | cmp
// cmp  := arith (('=='|'!='|'>'|'>='|'<'|'<=') arith)?   ← 不可串联
// arith:= term (('+'|'-') term)*
// term := factor (('*'|'/'|'%') factor)*
// factor := NUMBER | STRING | PATH | '(' or ')' | '-' factor
//
// 只判定合法性，不建 AST：这里唯一的产物是「后端会不会拒绝」。

interface Parser {
  toks: Tok[];
  i: number;
}

const COMPARISON_OPS = new Set(['==', '!=', '>', '>=', '<', '<=']);

function peek(p: Parser): Tok {
  return p.toks[p.i];
}

function advance(p: Parser): void {
  if (p.toks[p.i].kind !== 'eof') p.i++;
}

function isOp(tok: Tok, lit: string): boolean {
  return tok.kind === 'op' && tok.lit === lit;
}

function parseOr(p: Parser): ConditionSyntaxIssue | null {
  let issue = parseAnd(p);
  if (issue) return issue;
  while (isOp(peek(p), '||')) {
    advance(p);
    issue = parseAnd(p);
    if (issue) return issue;
  }
  return null;
}

function parseAnd(p: Parser): ConditionSyntaxIssue | null {
  let issue = parseUnary(p);
  if (issue) return issue;
  while (isOp(peek(p), '&&')) {
    advance(p);
    issue = parseUnary(p);
    if (issue) return issue;
  }
  return null;
}

function parseUnary(p: Parser): ConditionSyntaxIssue | null {
  if (isOp(peek(p), '!')) {
    advance(p);
    return parseUnary(p);
  }
  return parseComparison(p);
}

function parseComparison(p: Parser): ConditionSyntaxIssue | null {
  const issue = parseArithmetic(p);
  if (issue) return issue;
  const tok = peek(p);
  if (tok.kind !== 'op' || !COMPARISON_OPS.has(tok.lit)) return null;
  advance(p);
  return parseArithmetic(p);
}

function parseArithmetic(p: Parser): ConditionSyntaxIssue | null {
  const issue = parseTerm(p);
  if (issue) return issue;
  for (;;) {
    const tok = peek(p);
    if (!isOp(tok, '+') && !isOp(tok, '-')) return null;
    advance(p);
    const right = parseTerm(p);
    if (right) return right;
  }
}

function parseTerm(p: Parser): ConditionSyntaxIssue | null {
  const issue = parseFactor(p);
  if (issue) return issue;
  for (;;) {
    const tok = peek(p);
    if (!isOp(tok, '*') && !isOp(tok, '/') && !isOp(tok, '%')) return null;
    advance(p);
    const right = parseFactor(p);
    if (right) return right;
  }
}

function parseFactor(p: Parser): ConditionSyntaxIssue | null {
  const tok = peek(p);
  switch (tok.kind) {
    case 'number':
    case 'string':
    case 'path':
      advance(p);
      return null;
    case 'lparen': {
      advance(p);
      const issue = parseOr(p);
      if (issue) return issue;
      const closing = peek(p);
      if (closing.kind !== 'rparen') {
        return { message: `缺少右括号 )（位置 ${closing.pos}）`, pos: closing.pos };
      }
      advance(p);
      return null;
    }
    default:
      if (isOp(tok, '-')) {
        advance(p);
        return parseFactor(p);
      }
      // 缺右操作数（`hp >`）在这里撞到 EOF。后端此处打印的是 `意外的 token ""`，
      // 编辑器里照抄会像是工具自己出了错，故只有这一处措辞与后端不同。
      if (tok.kind === 'eof') {
        return { message: `表达式在位置 ${tok.pos} 意外结束，缺少操作数`, pos: tok.pos };
      }
      return { message: `意外的 token ${quote(tok.lit)}（位置 ${tok.pos}）`, pos: tok.pos };
  }
}

/** 对齐 Go `%q` 的字符串形态（双引号）。条件文法里字符串字面量不支持转义，无需转义处理。 */
function quote(text: string): string {
  return `"${text}"`;
}

/** 对齐 Go `%q` 作用于单字节的形态（单引号）。 */
function charQuote(char: string): string {
  return `'${char}'`;
}
