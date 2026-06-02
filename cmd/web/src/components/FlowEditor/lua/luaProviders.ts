/**
 * Monaco Editor 的 Lua 智能补全 / 悬停 / 函数签名 provider。
 *
 * 注册方式：在 LuaForm 的 onMount 回调中调用 `registerLuaProviders(monaco)`，
 * 该函数内部使用全局标记位避免重复注册（Monaco 没有去重机制，重复注册会让一个 hover 弹多次）。
 */

import type { Monaco } from '@monaco-editor/react';
import type { editor, languages, IRange, Position } from 'monaco-editor';
import { LUA_MODULES, getLuaFunction, getLuaModule, renderDoc, renderSignature } from './luaApiSpec';
import { protoRegistry } from '../proto/ProtoRegistry';
import type { ProtoField } from '@/types/proto';

let registered = false;

export function registerLuaProviders(monaco: Monaco): void {
  if (registered) return;
  registered = true;

  // 1. 补全：模块名 / 模块成员 / Proto 消息名/字段名
  monaco.languages.registerCompletionItemProvider('lua', {
    triggerCharacters: ['.', ':', ' ', '"', "'", ...Array.from('abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_')],
    provideCompletionItems: (model, position) => {
      const word = model.getWordUntilPosition(position);
      const range: IRange = {
        startLineNumber: position.lineNumber,
        endLineNumber: position.lineNumber,
        startColumn: word.startColumn,
        endColumn: word.endColumn,
      };
      const lineUntil = model
        .getLineContent(position.lineNumber)
        .slice(0, position.column - 1);

      const protoReady = protoRegistry.isLoaded();

      // ── Proto 补全（try-catch 防御：不影响已有模块补全） ──
      try {
        // 场景 1：消息名 — proto.create(" | network.*_request(..., "
        if (protoReady) {
          const protoNameMatch = lineUntil.match(
            /(?:proto\.(?:create|parse)|network\.(?:tcp_request|tcp_request_route|udp_request|udp_request_route|tcp_listen|udp_listen))\s*\(.*["']([\w.]*)$/
          );
          if (protoNameMatch) {
            const prefix = protoNameMatch[1] || '';
            // range 覆盖引号内的部分名，选中后整体替换（避免 Game.Game.XXX）
            const nameStartCol = position.column - prefix.length;
            const nameRange: IRange = {
              startLineNumber: position.lineNumber,
              endLineNumber: position.lineNumber,
              startColumn: nameStartCol,
              endColumn: position.column,
            };
            const messages = protoRegistry.listMessages(prefix);
            const suggestions: languages.CompletionItem[] = messages.map((m) => ({
              label: m.fullName,
              kind: monaco.languages.CompletionItemKind.Class,
              insertText: m.fullName,
              detail: `proto message · ${m.fields.length} 个字段`,
              documentation: {
                value: `**${m.fullName}**${m.comment ? ` — ${m.comment}` : ''}\n\n${m.fields.length > 0 ? m.fields.map((f) => `- \`${f.name}\`: \`${f.type}\`${f.repeated ? ' (repeated)' : ''}${f.comment ? ` — ${f.comment}` : ''}`).join('\n') : '（无字段）'}`,
              },
              range: nameRange,
            }));
            return { suggestions };
          }
        }

        // 场景 2：字段名 — proto.set_field(msg, " | proto.get_field(msg, "
        if (protoReady) {
          const fieldAccessMatch = lineUntil.match(
            /proto\.(?:set_field|get_field)\s*\(\s*(\w+)\s*,\s*["'](\w*)$/
          );
          if (fieldAccessMatch) {
            const varName = fieldAccessMatch[1];
            const prefix = fieldAccessMatch[2] || '';
            const msgType = resolveVarProtoType(model, varName, position);
            if (msgType) {
              const msg = protoRegistry.lookupMessage(msgType);
              if (msg) {
                // range 覆盖引号内已输入的部分字段名
                const fStartCol = position.column - prefix.length;
                const fieldRange: IRange = {
                  startLineNumber: position.lineNumber,
                  endLineNumber: position.lineNumber,
                  startColumn: fStartCol,
                  endColumn: position.column,
                };
                const suggestions: languages.CompletionItem[] = msg.fields
                  .filter((f) => !prefix || f.name.toLowerCase().startsWith(prefix.toLowerCase()))
                  .map((f) => ({
                    label: f.name,
                    kind: monaco.languages.CompletionItemKind.Field,
                    insertText: f.name,
                    detail: `${f.type} (${f.kind}${f.repeated ? ', repeated' : ''}${f.optional ? ', optional' : ''}${f.comment ? ` — ${f.comment}` : ''}`,
                    documentation: buildFieldDoc(f),
                    range: fieldRange,
                  }));
                return { suggestions };
              }
            }
          }
        }

        // 场景 3：proto.create 表参内字段名
        if (protoReady) {
          const msgName = findContainingProtoCreateTable(model, position, lineUntil);
          if (msgName) {
            const prefix = lineUntil.match(/\b(\w*)$/)?.[1] || '';
            const msg = protoRegistry.lookupMessage(msgName);
            if (msg) {
              const suggestions: languages.CompletionItem[] = msg.fields
                .filter((f) => !prefix || f.name.toLowerCase().startsWith(prefix.toLowerCase()))
                .map((f) => ({
                  label: f.name,
                  kind: monaco.languages.CompletionItemKind.Field,
                  insertText: `${f.name} = `,
                  detail: `${f.type} (${f.kind}${f.repeated ? ', repeated' : ''})${f.comment ? ` — ${f.comment}` : ''}`,
                  documentation: buildFieldDoc(f),
                  range,
                }));
              return { suggestions };
            }
          }
        }
      } catch (e) { console.warn('[luaProviders] proto completion error:', e); }

      // 1.1 `<module>.<partial>` → 列出该模块全部/过滤后的函数
      const moduleAccess = lineUntil.match(/(\w+)[.:](\w*)$/);
      if (moduleAccess) {
        const mod = getLuaModule(moduleAccess[1]);
        if (mod) {
          const partial = moduleAccess[2] || '';
          // range 覆盖 `.` 后的部分名
          const funcStartCol = position.column - partial.length;
          const funcRange: IRange = {
            startLineNumber: position.lineNumber,
            endLineNumber: position.lineNumber,
            startColumn: funcStartCol,
            endColumn: position.column,
          };
          const suggestions: languages.CompletionItem[] = mod.functions
            .filter((fn) => !partial || fn.name.toLowerCase().startsWith(partial.toLowerCase()))
            .map((fn) => ({
              label: fn.name,
              kind: monaco.languages.CompletionItemKind.Function,
              insertText: `${fn.name}(${fn.params.map((p) => p.name).join(', ')})`,
              documentation: { value: renderDoc(fn) },
              detail: `${mod.name}.${fn.name}${renderSignature(fn)} → ${fn.returns}`,
              range: funcRange,
            }));
          return { suggestions };
        }
      }

      // 1.2 顶层：列出模块名 + 关键字 + require 片段
      const topSuggestions: languages.CompletionItem[] = LUA_MODULES.map((m) => ({
        label: m.name,
        kind: monaco.languages.CompletionItemKind.Module,
        insertText: m.name,
        documentation: { value: m.summary },
        detail: `Lua module · ${m.functions.length} 个函数`,
        range,
      }));
      // 常用 require 片段
      for (const m of LUA_MODULES) {
        topSuggestions.push({
          label: `require('${m.name}')`,
          kind: monaco.languages.CompletionItemKind.Snippet,
          insertText: `local ${m.name} = require('${m.name}')`,
          documentation: { value: `导入 ${m.name} 模块` },
          range,
        });
      }
      return { suggestions: topSuggestions };
    },
  });

  // 2. 悬停文档
  monaco.languages.registerHoverProvider('lua', {
    provideHover: (model, position) => {
      const lineContent = model.getLineContent(position.lineNumber);
      const word = model.getWordAtPosition(position);
      if (!word) return null;
      // 解析 `<module>.<name>` 形式
      const beforeWord = lineContent
        .slice(0, word.startColumn - 1)
        .match(/(\w+)[.:]\s*$/);
      if (beforeWord) {
        const fn = getLuaFunction(beforeWord[1], word.word);
        if (fn) {
          return {
            range: {
              startLineNumber: position.lineNumber,
              endLineNumber: position.lineNumber,
              startColumn: word.startColumn,
              endColumn: word.endColumn,
            },
            contents: [{ value: renderDoc(fn) }],
          };
        }
      }
      // 模块名本身
      const mod = getLuaModule(word.word);
      if (mod) {
        return {
          range: {
            startLineNumber: position.lineNumber,
            endLineNumber: position.lineNumber,
            startColumn: word.startColumn,
            endColumn: word.endColumn,
          },
          contents: [
            { value: `**${mod.name}** — ${mod.summary}` },
            { value: `${mod.functions.length} 个函数（输入 \`${mod.name}.\` 查看完整列表）` },
          ],
        };
      }
      return null;
    },
  });

  // 3. 函数签名（参数提示）
  monaco.languages.registerSignatureHelpProvider('lua', {
    signatureHelpTriggerCharacters: ['(', ','],
    signatureHelpRetriggerCharacters: [','],
    provideSignatureHelp: (model, position) => parseSignatureHelp(model, position),
  });
}

function parseSignatureHelp(
  model: editor.ITextModel,
  position: Position,
): languages.SignatureHelpResult | null {
  // 向左扫描到匹配的 `(`，记录在它之前的标识符链 `module.name` 与当前已输入参数序号
  const lineContent = model.getLineContent(position.lineNumber);
  const before = lineContent.slice(0, position.column - 1);

  let depth = 0;
  let activeParam = 0;
  let cutAt = -1;
  for (let i = before.length - 1; i >= 0; i--) {
    const ch = before[i];
    if (ch === ')') depth++;
    else if (ch === '(') {
      if (depth === 0) {
        cutAt = i;
        break;
      }
      depth--;
    } else if (ch === ',' && depth === 0) {
      activeParam++;
    }
  }
  if (cutAt < 0) return null;
  // cutAt 之前是 `module.name`
  const callMatch = before.slice(0, cutAt).match(/(\w+)[.:](\w+)\s*$/);
  if (!callMatch) return null;
  const fn = getLuaFunction(callMatch[1], callMatch[2]);
  if (!fn) return null;

  return {
    value: {
      activeSignature: 0,
      activeParameter: Math.min(activeParam, Math.max(0, fn.params.length - 1)),
      signatures: [
        {
          label: `${fn.module}.${fn.name}${renderSignature(fn)} → ${fn.returns}`,
          documentation: { value: fn.summary + (fn.detail ? `\n\n${fn.detail}` : '') },
          parameters: fn.params.map((p) => ({
            label: p.optional ? `[${p.name}]` : p.name,
            documentation: { value: `_${p.type}_ — ${p.doc}` },
          })),
        },
      ],
    },
    dispose: () => undefined,
  };
}

// ── Proto 补全辅助函数 ──

function escapeRegex(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function buildFieldDoc(f: ProtoField): { value: string } {
  const lines: string[] = [`**${f.name}**: \`${f.type}\``, `- Kind: ${f.kind}`];
  if (f.repeated) lines.push('- Repeated (list)');
  if (f.optional) lines.push('- Optional');
  if (f.messageName) lines.push(`- Message: \`${f.messageName}\``);
  if (f.enumName) lines.push(`- Enum: \`${f.enumName}\``);
  if (f.comment) lines.push(`- ${f.comment}`);
  if (f.kind === 'map') lines.push(`- Map: \`${f.mapKey}\` → \`${f.mapValue}\``);
  return { value: lines.join('\n') };
}

/** 从光标向上扫描，找到变量最近的 proto.create/parse 赋值 */
function resolveVarProtoType(
  model: editor.ITextModel,
  varName: string,
  position: Position,
): string | null {
  const re = new RegExp(
    `local\\s+${escapeRegex(varName)}\\s*=\\s*proto\\.(?:create|parse)\\s*\\(\\s*["']([\\w.]+)["']`
  );
  for (let line = position.lineNumber; line >= 1; line--) {
    const match = model.getLineContent(line).match(re);
    if (match) return match[1];
  }
  return null;
}

/** 从光标向上追踪花括号深度，找到所属 proto.create 调用的消息名 */
function findContainingProtoCreateTable(
  model: editor.ITextModel,
  position: Position,
  lineUntil: string,
): string | null {
  // 快速路径：单行 proto.create("Msg", { ...
  const singleLine = lineUntil.match(
    /proto\.create\s*\(\s*["']([\w.]+)["']\s*,\s*\{[^}]*$/
  );
  if (singleLine) return singleLine[1];

  // 多行：从光标向上追踪 { } 深度
  let depth = 0;
  for (let line = position.lineNumber; line >= 1; line--) {
    const content = model.getLineContent(line);
    const startCol = line === position.lineNumber ? position.column - 1 : content.length;
    for (let col = startCol; col >= 1; col--) {
      const ch = content[col - 1];
      if (ch === '}') depth++;
      else if (ch === '{') {
        depth--;
        if (depth < 0) {
          const before = content.slice(0, col - 1);
          const match = before.match(/proto\.create\s*\(\s*["']([\w.]+)["']\s*,\s*$/);
          return match ? match[1] : null;
        }
      }
    }
  }
  return null;
}
