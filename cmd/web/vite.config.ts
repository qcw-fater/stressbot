import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';

export default defineConfig({
  base: '/stressbot/',
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/sbot': {
        target: process.env.STRESSBOT_ADMIN ?? 'http://localhost:7718',
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
          if (!id.includes('node_modules/')) return;
          if (id.includes('monaco-editor')) return 'vendor-monaco';
          if (id.includes('echarts') || id.includes('zrender')) return 'vendor-echarts';
          if (id.includes('@xyflow')) return 'vendor-flow';
          if (id.includes('protobufjs')) return 'vendor-protobuf';
        },
      },
    },
  },
});
