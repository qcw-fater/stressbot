/**
 * Monaco Editor 的 Lua 智能补全 / 悬停 / 函数签名 provider。
 *
 * 注册方式：在 LuaForm 的 onMount 回调中调用 `registerLuaProviders(monaco)`，
 * 该函数内部使用全局标记位避免重复注册（Monaco 没有去重机制，重复注册会让一个 hover 弹多次）。
 */

import type { Monaco } from '@monaco-editor/react';
import type { editor, languages, IRange, Position } from 'monaco-editor';
import { LUA_MODULES, getLuaFunction, getLuaModule, renderDoc, renderSignature } from './luaApiSpec';

let registered = false;

export function registerLuaProviders(monaco: Monaco): void {
  if (registered) return;
  registered = true;

  // 1. 补全：模块名 / 模块成员 / 局部变量名
  monaco.languages.registerCompletionItemProvider('lua', {
    triggerCharacters: ['.', ':', ' ', '"', "'"],
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

      // 1.1 `<module>.` 或 `<module>:` → 列出该模块全部函数
      const moduleAccess = lineUntil.match(/(\w+)[.:]\s*$/);
      if (moduleAccess) {
        const mod = getLuaModule(moduleAccess[1]);
        if (mod) {
          const suggestions: languages.CompletionItem[] = mod.functions.map((fn) => ({
            label: fn.name,
            kind: monaco.languages.CompletionItemKind.Function,
            insertText: `${fn.name}(${fn.params.map((p) => p.name).join(', ')})`,
            documentation: { value: renderDoc(fn) },
            detail: `${mod.name}.${fn.name}${renderSignature(fn)} → ${fn.returns}`,
            range,
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
