import { defineConfig } from 'vitest/config';
import { loadEnv } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';

export default defineConfig({
  base: '/stressbot/',
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    // 默认 5s 对本仓库的组件测试不够：单跑都是几百毫秒，但全量并行时 jsdom 环境准备 +
    // antd/monaco 模块导入会把 userEvent 的交互链拖过阈值，于是 SetStateEditor、
    // ListenRefsTable、Toolbar.configBackup 这类测试在满负载下偶发超时（断言本身没问题）。
    // 放宽到 20s：真正挂死的测试仍会被拦住，机器负载不再伪造失败。
    testTimeout: 20_000,
    hookTimeout: 20_000,
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
      '@protobufjs/inquire': path.resolve(__dirname, 'src/shims/protobufjs-inquire.cjs'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/sbot': {
        target:
          loadEnv('development', __dirname, 'STRESSBOT_').STRESSBOT_ADMIN ??
          'http://localhost:7718',
        changeOrigin: true,
      },
    },
    fs: {
      allow: ['../..'],
    },
  },
  build: {
    outDir: 'dist',
    // sourcemap 会把 dist 体积翻倍（.map 与源码同大），常规发布不产出；
    // 排查线上问题时执行 npx vite build --sourcemap 单独出带映射的包
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks(id) {
          const normalizedId = id.replace(/\\/g, '/');
          // __vitePreload 助手被所有动态 import 共享，必须钉在一个极小的独立 chunk；
          // 放任默认分配可能落进 vendor-monaco，导致首屏为加载助手而静态拉取整个 monaco 本体
          if (normalizedId.includes('preload-helper')) return 'preload-runtime';
          if (!normalizedId.includes('node_modules/')) return;

          // @monaco-editor/react 胶水与 loader 留给默认分包（跟宿主 chunk）；
          // 若并入 vendor-monaco，rolldown 可能把 __vitePreload 助手也放进该 chunk，
          // 导致首屏为加载助手而静态拉取整个 3.7MB 的 monaco 本体
          if (normalizedId.includes('@monaco-editor/')) return;
          if (normalizedId.includes('monaco-editor')) return 'vendor-monaco';
          if (normalizedId.includes('@xyflow')) return 'vendor-flow';
          if (normalizedId.includes('protobufjs') || normalizedId.includes('@protobufjs'))
            return 'vendor-protobuf';
          if (normalizedId.includes('react-dom') || normalizedId.includes('react/'))
            return 'vendor-react';
          if (
            normalizedId.includes('dayjs') ||
            normalizedId.includes('zustand') ||
            normalizedId.includes('zundo')
          )
            return 'vendor-utils';

          if (normalizedId.includes('zrender')) return 'vendor-zrender';
          if (normalizedId.includes('echarts-for-react')) return 'vendor-echarts-react';
          const echartsLib = normalizedId.match(/node_modules\/echarts\/lib\/([^/]+)/);
          if (echartsLib) return `vendor-echarts-${echartsLib[1]}`;
          if (normalizedId.includes('node_modules/echarts/')) return 'vendor-echarts-entry';

          if (normalizedId.includes('@ant-design/icons')) return 'vendor-antd-icons';
          if (normalizedId.includes('@ant-design')) return 'vendor-antd-deps';
          const antdModule = normalizedId.match(/node_modules\/antd\/(?:es|lib)\/([^/]+)/);
          if (antdModule) return `vendor-antd-${antdModule[1].replace(/^_/, 'internal-')}`;

          if (normalizedId.includes('rc-table') || normalizedId.includes('rc-virtual-list'))
            return 'vendor-rc-table';
          if (
            normalizedId.includes('rc-dialog') ||
            normalizedId.includes('rc-drawer') ||
            normalizedId.includes('rc-trigger') ||
            normalizedId.includes('rc-tooltip')
          )
            return 'vendor-rc-overlay';
          if (
            normalizedId.includes('rc-select') ||
            normalizedId.includes('rc-input') ||
            normalizedId.includes('rc-field-form')
          )
            return 'vendor-rc-forms';
          if (normalizedId.includes('rc-')) return 'vendor-rc';
        },
      },
    },
  },
});
