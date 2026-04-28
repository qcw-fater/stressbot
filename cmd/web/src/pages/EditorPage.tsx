/**
 * 全屏编辑器页面。
 */

import { FlowEditor } from '@/components/FlowEditor';

export function EditorPage() {
  return (
    <div style={{ width: '100vw', height: '100vh' }}>
      <FlowEditor autoLoadDefault />
    </div>
  );
}
