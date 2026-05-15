/**
 * Proto 加载器：按优先级从多个来源加载 .proto 文件。
 *
 * 设计文档 §9.1；T2 阶段调整后的 `static` 加载链：
 *
 *   IndexedDB (用户上传) → Vite 中间件 /conf/proto/* → import.meta.glob 兜底
 *
 * 这样开发期 / 生产期 / 离线包装期都能拿到 proto；上传后调用 `reloadProtos()` 立即生效。
 *
 * 编译期 glob 兜底：
 *   - 通过 `import.meta.glob` 把仓库根 `conf/proto/*.proto` 内联为原始字符串，
 *     避免依赖 dev server 中间件 / HTTP fetch（曾因 SPA fallback、路由顺序导致加载失败）。
 *   - HMR 友好：修改 .proto 文件 vite 会自动重启加载。
 *   - 缺点：所有 proto 文本会打包进 bundle，但生产模式应优先使用 IDB / Admin。
 */

import * as protobuf from 'protobufjs';
import { listProto } from '@/services/resourcesStore';
import { API_PREFIX } from '@/services/env';
import { fetchBaselineProtoIndex, fetchBaselineProtoContent } from '@/services/baselineApi';

// 关键：路径相对当前文件 (web/src/components/FlowEditor/proto/ProtoLoader.ts) 到 stressbot/conf/proto。
// 必须是字面量，不能动态拼接（vite 编译期解析）。
// query=?raw + import=default 让每个文件以原始字符串导入；eager=true 同步注入。
const STATIC_PROTO_FILES = import.meta.glob('../../../../../conf/proto/*.proto', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

export type ProtoSource =
  | { kind: 'static' } // A：Vite 编译期内联
  | { kind: 'api'; baseUrl: string } // B：Go monitor HTTP
  | { kind: 'files'; files: File[] }; // C：用户上传

export interface LoadResult {
  root: protobuf.Root;
  /** 用于缓存键的 hash（基于所有 .proto 文件文本拼接的 SHA-1） */
  hash: string;
  /** 实际加载的文件名 */
  files: string[];
}

export async function loadProtos(source: ProtoSource): Promise<LoadResult> {
  switch (source.kind) {
    case 'static':
      return loadFromInline();
    case 'api':
      return loadFromHttp(source.baseUrl, `${API_PREFIX}/proto/files`, `${API_PREFIX}/proto/file/`);
    case 'files':
      return loadFromFiles(source.files);
  }
}

/** A：按 IndexedDB → /conf/proto/* → import.meta.glob 顺序加载。
 *      任一来源得到非空文件清单即视为成功并返回；其余跳过。 */
async function loadFromInline(): Promise<LoadResult> {
  // 1) IndexedDB：用户在"资源管理"中上传的 proto。优先级最高，离线 / 生产环境都能用。
  try {
    const userProtos = await listProto();
    if (userProtos.length > 0) {
      const sources: Record<string, string> = {};
      const files: string[] = [];
      for (const f of userProtos) {
        sources[f.name] = f.content;
        files.push(f.name);
      }
      files.sort();
      console.log(`[ProtoLoader] 通过 IndexedDB 加载（${files.length} 个文件）`);
      return parseAll(sources, files);
    }
  } catch (e) {
    console.warn(`[ProtoLoader] IndexedDB 读取失败，回退到 /conf/proto/：`, (e as Error).message);
  }

  // 2) 走 baselineApi（Admin 服务器或 Vite 代理提供）
  try {
    const r = await loadFromBaselineApi();
    console.log(`[ProtoLoader] 通过 baselineApi 加载成功（${r.files.length} 个文件）`);
    return r;
  } catch (e) {
    console.warn(`[ProtoLoader] /conf/proto/ 加载失败，尝试 import.meta.glob 兜底：`, (e as Error).message);
  }

  // 3) 兜底：编译期 glob 注入
  const sources: Record<string, string> = {};
  const files: string[] = [];
  for (const [path, content] of Object.entries(STATIC_PROTO_FILES)) {
    const name = path.split(/[\\/]/).pop()!;
    sources[name] = content;
    files.push(name);
  }
  if (files.length === 0) {
    throw new Error(
      `所有 proto 来源均为空：IndexedDB / /conf/proto/ / import.meta.glob('../../../../../conf/proto/*.proto') 均未命中。请确认资源已上传，或 stressbot/conf/proto 下存在 .proto 文件，且 vite server.fs.allow 已包含 '..'。`,
    );
  }
  files.sort();
  console.log(`[ProtoLoader] 通过 import.meta.glob 加载（${files.length} 个文件）`);
  return parseAll(sources, files);
}

async function loadFromBaselineApi(): Promise<LoadResult> {
  const files = await fetchBaselineProtoIndex();
  if (files.length === 0) throw new Error('proto index 为空');
  const sources: Record<string, string> = {};
  await Promise.all(
    files.map(async (name) => {
      const content = await fetchBaselineProtoContent(name);
      if (content === null) throw new Error(`加载 ${name} 失败`);
      sources[name] = content;
    }),
  );
  return parseAll(sources, files);
}

async function loadFromHttp(
  baseUrl: string,
  indexPath: string,
  filePathPrefix?: string,
): Promise<LoadResult> {
  const indexUrl = baseUrl.replace(/\/$/, '') + indexPath;
  const resp = await fetch(indexUrl);
  if (!resp.ok) throw new Error(`无法获取 proto 文件列表 (${indexUrl}): HTTP ${resp.status}`);
  const files = (await resp.json()) as string[];
  const sources: Record<string, string> = {};
  await Promise.all(
    files.map(async (name) => {
      const url = filePathPrefix
        ? baseUrl.replace(/\/$/, '') + filePathPrefix + name
        : baseUrl.replace(/\/$/, '') + '/proto/' + name;
      const r = await fetch(url);
      if (!r.ok) throw new Error(`加载 ${name} 失败：HTTP ${r.status}`);
      sources[name] = await r.text();
    }),
  );
  return parseAll(sources, files);
}

async function loadFromFiles(files: File[]): Promise<LoadResult> {
  const sources: Record<string, string> = {};
  for (const f of files) sources[f.name] = await f.text();
  return parseAll(
    sources,
    files.map((f) => f.name),
  );
}

async function parseAll(
  sources: Record<string, string>,
  fileList: string[],
): Promise<LoadResult> {
  const root = new protobuf.Root();

  // 重写 fetch：让 root.load 内部对 import 解析走内存查找，避免向后端发请求。
  // 路径形态可能是 "common.proto"、"player.proto"、"google/protobuf/empty.proto" 等，
  // 用 basename 兜底匹配；google/* 返回空 proto3 让 protobufjs 跳过。
  (root as unknown as { fetch: (path: string, cb: (err: Error | null, src?: string) => void) => void }).fetch = (
    filename,
    callback,
  ) => {
    if (filename in sources) {
      callback(null, sources[filename]);
      return;
    }
    const base = filename.split('/').pop() ?? filename;
    if (base in sources) {
      callback(null, sources[base]);
      return;
    }
    if (filename.startsWith('google/')) {
      callback(null, 'syntax = "proto3";\n');
      return;
    }
    console.warn(
      `[ProtoLoader] import 找不到：${filename}（候选：${Object.keys(sources).slice(0, 8).join(', ')}…）`,
    );
    callback(new Error('not found: ' + filename));
  };

  type LoadOpts = { keepCase?: boolean; alternateCommentMode?: boolean; preferTrailingComment?: boolean };
  type LoadFn = (
    filename: string | string[],
    options?: LoadOpts,
    callback?: (err: Error | null, root?: protobuf.Root) => void,
  ) => Promise<protobuf.Root>;
  const load = (root.load as unknown) as LoadFn;
  const errors: string[] = [];

  const parseOpts = { keepCase: true, alternateCommentMode: true, preferTrailingComment: true };

  // 第一阶段：root.load 整体加载（按 import 依赖图自动排序）
  await new Promise<void>((resolve) => {
    load.call(root, fileList, parseOpts, (err) => {
      if (err) {
        // 不致命：root.load 遇错会中止，下方逐文件兜底 parse 仍可能补齐
        errors.push(`load 阶段：${err.message}`);
      }
      resolve();
    });
  });

  // 第二阶段：兜底逐文件 parse —— root.load 中断时仍然可以补齐剩余文件。
  // 同名 message 会抛 "duplicate name"，安全跳过即可（说明 root.load 已注册过它）。
  let registeredAfterLoad = countMessages(root);
  for (const name of fileList) {
    try {
      protobuf.parse(sources[name], root, parseOpts);
    } catch (e) {
      const msg = (e as Error).message;
      if (msg.includes('duplicate name')) continue;
      errors.push(`parse ${name}：${msg}`);
    }
  }
  const registeredAfterFallback = countMessages(root);

  // 第三阶段：resolve type lookup
  try {
    root.resolveAll();
  } catch (e) {
    errors.push(`resolveAll：${(e as Error).message}`);
  }

  console.log(
    `[ProtoLoader] 加载完成：${fileList.length} 个 .proto 文件、${countMessages(root)} 个 message（load 阶段 ${registeredAfterLoad} → 兜底后 ${registeredAfterFallback}）`,
  );
  if (errors.length > 0) {
    console.warn(`[ProtoLoader] ${errors.length} 处警告：`, errors.slice(0, 8));
  }

  const concat = fileList.map((f) => sources[f] ?? '').join('\n---\n');
  const hash = await sha1(concat);
  return { root, hash, files: fileList };
}

/** 简单递归计数 root 下的 message 类型，用于诊断加载完成度。
 *  注意：protobufjs 的 NamespaceBase 仅在 .d.ts 中以类型存在，运行时未导出 ——
 *        必须用 protobuf.Namespace（运行时类）或鸭子类型 nestedArray 判断。 */
function countMessages(root: protobuf.Root): number {
  let n = 0;
  const walk = (ns: protobuf.Namespace) => {
    if (!ns.nestedArray) return;
    for (const child of ns.nestedArray) {
      if (child instanceof protobuf.Type) n++;
      if ((child as { nestedArray?: unknown }).nestedArray !== undefined) {
        walk(child as protobuf.Namespace);
      }
    }
  };
  walk(root);
  return n;
}

async function sha1(text: string): Promise<string> {
  const enc = new TextEncoder().encode(text);
  const buf = await crypto.subtle.digest('SHA-1', enc);
  return Array.from(new Uint8Array(buf))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
}
