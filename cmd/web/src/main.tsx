import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { ConfigProvider, theme as antdTheme } from 'antd';
import zhCN from 'antd/locale/zh_CN';

import { App } from './App';
import { useEditorStore } from './components/FlowEditor/store/editorStore';
import * as services from './services';
import './styles/global.css';
import '@xyflow/react/dist/style.css';

// dev 期把 services 暴露到 window，便于浏览器 console 手动调用：
//   stressbot.tasksApi.listTasks()
//   stressbot.useRuntimeStore.getState().mode
if (import.meta.env.DEV) {
  (window as unknown as { stressbot?: typeof services }).stressbot = services;
}

function ThemedApp() {
  const mode = useEditorStore((s) => s.theme);
  return (
    <ConfigProvider
      locale={zhCN}
      theme={{
        algorithm: mode === 'dark' ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
      }}
    >
      <App />
    </ConfigProvider>
  );
}

const root = createRoot(document.getElementById('root')!);
root.render(
  <StrictMode>
    <ThemedApp />
  </StrictMode>,
);
