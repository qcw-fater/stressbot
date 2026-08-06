/**
 * Proto 与本地存储的"全量基线 gap-fill" + 依赖完整性校验工具。
 *
 * 设计目标：
 *   - 把基线 `/sbot/baseline/proto/` 中、本地存储（IDB）还没有的 proto 文件拉回并写入 IDB；
 *   - **不覆盖**本地存储中已存在的 proto（保护用户编辑稿）——与 scriptSync 同语义；
 *   - 对本地 proto 集合做 **import 依赖完整性校验**：任一 `import "X.proto";` 的目标不在集合内
 *     即视为残缺，由调用方（startTask）硬拦截，避免把缺依赖的任务下发到 Agent 编译失败。
 *
 * 为什么是「全量基线」而非「按 flow 引用」（与脚本不同）：
 *   - 脚本在 flow 里以**文件名**直接引用（actions[].script = "foo.lua"），可静态收集按需补；
 *   - proto 在 flow 里以**消息全名**引用（c2sProto = "Game.GmEventC2S"），无法反推所在文件，
 *     只能按基线索引全集补齐。好在 proto 通常上百个、各几 KB，全量补可承受。
 *
 * 调用点：TaskStartModal 打开（预览）、startTask 提交前（最终拦截）。
 *
 * 基线不可用时的策略（best-effort）：
 *   - 单机 / 离线 / Admin 未启时基线索引请求会失败（返回 null）。此时**不抛错**，跳过 gap-fill，
 *     信任本地 IDB 现有集合——后续 {@link missingProtoImports} 完整性校验仍会兜底拦截残缺集。
 *   - 这与脚本 gap-fill 一致：基线断网时不阻塞，由本地集合 + 完整性校验兜底。
 */

import { getProto, addProtoFromBaseline, type ResourceFile } from './resourcesStore';
import { fetchBaselineProtoIndex, fetchBaselineProtoContent } from './baselineApi';

export interface ProtoSyncResult {
  /** 本次真正从基线拉回并写入 IDB 的 proto 文件名（之前本地没有） */
  added: string[];
  /** 基线有、本地 IDB 已有，本次未动（保护用户编辑稿） */
  skipped: string[];
  /** 基线索引列出、但内容拉取失败（网络抖动 / 服务器不一致） */
  missing: string[];
  /** 基线索引是否成功获取；false 表示基线不可用（已跳过 gap-fill，交由完整性校验兜底） */
  indexAvailable: boolean;
}

/**
 * 把基线中、IDB 里还没有的 proto 全量拉回写入 IDB。不覆盖已存在项。
 *
 * 不并发限流：proto 通常上百个、各几 KB，浏览器一次性 fetch 可承受；且先 getProto 判存在，
 * IDB 已齐时零网络请求。
 */
export async function syncProtosToIdb(): Promise<ProtoSyncResult> {
  const index = await fetchBaselineProtoIndex();
  if (index === null) {
    // 基线不可用：不阻塞，跳过 gap-fill（完整性校验会兜底）
    return { added: [], skipped: [], missing: [], indexAvailable: false };
  }

  const added: string[] = [];
  const skipped: string[] = [];
  const missing: string[] = [];

  await Promise.all(
    index.map(async (name) => {
      const existed = await getProto(name);
      if (existed) {
        skipped.push(name);
        return;
      }
      const text = await fetchBaselineProtoContent(name);
      if (text === null) {
        missing.push(name);
        return;
      }
      await addProtoFromBaseline(name, text);
      added.push(name);
    }),
  );

  added.sort();
  skipped.sort();
  missing.sort();
  return { added, skipped, missing, indexAvailable: true };
}

const IMPORT_RE = /^\s*import\s+(?:public\s+|weak\s+)?"([^"]+)";/gm;

/**
 * 校验本地 proto 集合的 import 依赖是否自洽：任一 `import "X.proto";` 的目标文件不在集合内
 * 即为残缺。返回缺失的目标文件名（basename）列表，空数组表示完整。
 *
 * - `google/...` 前缀的 well-known types 由后端 protocompile 的 WithStandardImports 提供，跳过；
 * - 与后端 protox.Loader 一致：按 basename 匹配（`common.proto` 与 `sub/common.proto` 视作同文件）；
 * - 纯静态文本扫描，不依赖 protobufjs，可在提交前轻量拦截残缺集。
 *
 * 这是对「基线不可用 / IDB 残缺」的最终兜底：即便 gap-fill 跳过，残缺集也会在此被拦下，
 * 而不是带着缺依赖下发到 Agent 才编译失败（custom_activity.proto 找不到 custom_task.proto 那类错误）。
 */
export function missingProtoImports(protos: readonly ResourceFile[]): string[] {
  const names = new Set(protos.map((p) => p.name));
  const missing = new Set<string>();
  for (const p of protos) {
    for (const m of p.content.matchAll(IMPORT_RE)) {
      const spec = m[1];
      if (spec.startsWith('google/')) continue;
      const base = spec.split(/[\\/]/).pop() ?? spec;
      if (!names.has(base)) missing.add(base);
    }
  }
  return Array.from(missing).sort();
}
