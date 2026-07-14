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
        target: loadEnv('development', __dirname, 'STRESSBOT_').STRESSBOT_ADMIN
          ?? 'http://localhost:7718',
        changeOrigin: true,
      },
    },
    fs: {
      allow: ['..'],
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
    rollupOptions: {
      output: {
        manualChunks(id) {
          const normalizedId = id.replace(/\\/g, '/');
          if (!normalizedId.includes('node_modules/')) return;

          if (normalizedId.includes('monaco-editor')) return 'vendor-monaco';
          if (normalizedId.includes('@xyflow')) return 'vendor-flow';
          if (normalizedId.includes('protobufjs') || normalizedId.includes('@protobufjs')) return 'vendor-protobuf';
          if (normalizedId.includes('react-dom') || normalizedId.includes('react/')) return 'vendor-react';
          if (normalizedId.includes('dayjs') || normalizedId.includes('zustand') || normalizedId.includes('zundo')) return 'vendor-utils';

          if (normalizedId.includes('zrender')) return 'vendor-zrender';
          if (normalizedId.includes('echarts-for-react')) return 'vendor-echarts-react';
          const echartsLib = normalizedId.match(/node_modules\/echarts\/lib\/([^/]+)/);
          if (echartsLib) return `vendor-echarts-${echartsLib[1]}`;
          if (normalizedId.includes('node_modules/echarts/')) return 'vendor-echarts-entry';

          if (normalizedId.includes('@ant-design/icons')) return 'vendor-antd-icons';
          if (normalizedId.includes('@ant-design')) return 'vendor-antd-deps';
          const antdModule = normalizedId.match(/node_modules\/antd\/(?:es|lib)\/([^/]+)/);
          if (antdModule) return `vendor-antd-${antdModule[1].replace(/^_/, 'internal-')}`;

          if (normalizedId.includes('rc-table') || normalizedId.includes('rc-virtual-list')) return 'vendor-rc-table';
          if (normalizedId.includes('rc-dialog') || normalizedId.includes('rc-drawer') || normalizedId.includes('rc-trigger') || normalizedId.includes('rc-tooltip')) return 'vendor-rc-overlay';
          if (normalizedId.includes('rc-select') || normalizedId.includes('rc-input') || normalizedId.includes('rc-field-form')) return 'vendor-rc-forms';
          if (normalizedId.includes('rc-')) return 'vendor-rc';
        },
      },
    },
  },
});
