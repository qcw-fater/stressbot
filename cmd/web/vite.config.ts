import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';
import fs from 'node:fs';

// 把仓库根的 conf/ 通过中间件挂到 /conf/*，使编辑器可直接 fetch '/conf/flow.json'
// 而无需把 conf/ 复制进 web/public（坚持单一来源原则）。
//
// 路径换算：vite.config.ts 位于 cmd/web/，仓库根在 cmd/web/../.. = stressbot/，
// 所以 confRoot = stressbot/conf。早期版本误写成 '..' 一级，导致 /conf/proto/index.json
// 实际去读不存在的 cmd/conf 抛 ENOENT，前端收到 500。
function confMountPlugin(): Plugin {
  const confRoot = path.resolve(__dirname, '..', '..', 'conf');
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
            res.end(`failed to list proto (confRoot=${confRoot}): ${(e as Error).message}`);
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
            res.end(`failed to list scripts (confRoot=${confRoot}): ${(e as Error).message}`);
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
      // 默认转发到 Admin :8080（docs/admin-implementation.md 默认监听）；
      // 通过 STRESSBOT_ADMIN 环境变量可覆盖（例如 set STRESSBOT_ADMIN=http://10.0.0.5:8080）
      '/api': {
        target: process.env.STRESSBOT_ADMIN ?? 'http://localhost:8080',
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
