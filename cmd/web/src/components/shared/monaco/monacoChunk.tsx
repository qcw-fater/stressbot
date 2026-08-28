/**
 * Monaco 本地打包入口（懒加载 chunk 顶层，加载即完成初始化）。
 *
 * - loader.config({ monaco })：@monaco-editor/react 默认在运行时从 jsdelivr CDN
 *   拉取完整 Monaco（约 3MB），内网/离线环境不可用且首开阻塞；这里改为传入本
 *   chunk 打包的本地实例，与业务代码同源部署、同缓存策略。
 * - MonacoEnvironment.getWorker：按语言分流到 Vite `?worker` 切出的独立 worker
 *   文件，语法高亮/校验移出主线程；每个 worker 文件仅在对应语言首次使用时下载
 *   （Lua 走项目自带 luaSyntaxWorker，无 Monaco worker）。
 */

import Editor, { DiffEditor, loader } from '@monaco-editor/react';
import * as monaco from 'monaco-editor';
import editorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
import cssWorker from 'monaco-editor/esm/vs/language/css/css.worker?worker';
import htmlWorker from 'monaco-editor/esm/vs/language/html/html.worker?worker';
import jsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker';
import tsWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker';

self.MonacoEnvironment = {
  getWorker(_workerId: string, label: string): Worker {
    if (label === 'json') return new jsonWorker();
    if (label === 'css' || label === 'scss' || label === 'less') return new cssWorker();
    if (label === 'html' || label === 'handlebars' || label === 'razor') return new htmlWorker();
    if (label === 'typescript' || label === 'javascript') return new tsWorker();
    return new editorWorker();
  },
};

loader.config({ monaco });

export const MonacoEditor = Editor;
export const MonacoDiffEditor = DiffEditor;
