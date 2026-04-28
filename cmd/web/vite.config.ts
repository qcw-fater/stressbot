import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';
import fs from 'node:fs';

// 把仓库根的 conf/ 通过中间件挂到 /conf/*，使编辑器可直接 fetch '/conf/flow.json'
// 而无需把 conf/ 复制进 web/public（坚持单一来源原则）。
function confMountPlugin(): Plugin {
  const confRoot = path.resolve(__dirname, '..', 'conf');
  return {
    name: 'stressbot-conf-mount',
    configureServer(server) {
      server.middlewares.use('/conf', (req, res, next) => {
        const url = req.url ?? '/';
        // 列出文件清单：/conf/proto/index.json
        if (url.startsWith('/proto/index.json')) {
          try {
            const dir = path.join(confRoot, 'proto');
            const files = fs.readdirSync(dir).filter((f) => f.endsWith('.proto'));
            res.setHeader('Content-Type', 'application/json; charset=utf-8');
            res.end(JSON.stringify(files));
            return;
          } catch (e) {
            res.statusCode = 500;
            res.end(`failed to list proto: ${(e as Error).message}`);
            return;
          }
        }
        if (url.startsWith('/scripts/index.json')) {
          try {
            const dir = path.join(confRoot, 'scripts');
            const files = fs.readdirSync(dir).filter((f) => f.endsWith('.lua'));
            res.setHeader('Content-Type', 'application/json; charset=utf-8');
            res.end(JSON.stringify(files));
            return;
          } catch (e) {
            res.statusCode = 500;
            res.end(`failed to list scripts: ${(e as Error).message}`);
            return;
          }
        }

        // 解析路径，过滤路径穿越
        const safe = path.normalize(decodeURIComponent(url.split('?')[0])).replace(/^[/\\]+/, '');
        const fullPath = path.join(confRoot, safe);
        if (!fullPath.startsWith(confRoot)) {
          res.statusCode = 403;
          res.end('forbidden');
          return;
        }
        if (!fs.existsSync(fullPath) || fs.statSync(fullPath).isDirectory()) {
          next();
          return;
        }
        const ext = path.extname(fullPath).toLowerCase();
        const mime: Record<string, string> = {
          '.json': 'application/json; charset=utf-8',
          '.proto': 'text/plain; charset=utf-8',
          '.lua': 'text/plain; charset=utf-8',
        };
        res.setHeader('Content-Type', mime[ext] ?? 'application/octet-stream');
        fs.createReadStream(fullPath).pipe(res);
      });
    },
  };
}

export default defineConfig({
  plugins: [react(), confMountPlugin()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:6060',
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
  },
});
