/**
 * Monaco 编辑器的懒加载门面。
 *
 * 完整的 monaco-editor（约 3MB）与各语言 worker 都收在 monacoChunk 里，
 * 只有第一个编辑器真正挂载时才加载该 chunk；MonacoLazy 自身极小，
 * 可以安全地被急加载模块（如 LuaForm）引用而不拖累首屏。
 * monacoChunk 顶层先于组件求值完成 loader.config 与 worker 注册，
 * 因此 <Editor> 挂载时 loader 已指向本地打包实例（不再走 jsdelivr CDN）。
 */

import { Spin } from 'antd';
import { lazy, Suspense } from 'react';
import type { DiffEditorProps, EditorProps } from '@monaco-editor/react';

const MonacoEditorLazy = lazy(async () => {
  const m = await import('./monacoChunk');
  return { default: m.MonacoEditor };
});
const MonacoDiffEditorLazy = lazy(async () => {
  const m = await import('./monacoChunk');
  return { default: m.MonacoDiffEditor };
});

function LoadingBox({ height }: { height?: EditorProps['height'] | DiffEditorProps['height'] }) {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        height: height ?? '100%',
        minHeight: 60,
      }}
    >
      <Spin size="small" />
    </div>
  );
}

export function MonacoEditor(props: EditorProps) {
  return (
    <Suspense fallback={<LoadingBox height={props.height} />}>
      <MonacoEditorLazy {...props} />
    </Suspense>
  );
}

export function MonacoDiffEditor(props: DiffEditorProps) {
  return (
    <Suspense fallback={<LoadingBox height={props.height} />}>
      <MonacoDiffEditorLazy {...props} />
    </Suspense>
  );
}
