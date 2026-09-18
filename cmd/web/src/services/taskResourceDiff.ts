import type { FlowJson } from '@/components/FlowEditor/codec/flowToJson';
import { fetchBaselineResource } from './baselineApi';
import { connNameToCodecFileName, codecFileNameToConnNameStrict } from './codecConnections';
import { collectFlowScriptNames } from './scriptSync';
import {
  getCodecSchema,
  getErrorMap,
  getScript,
  hasSyncDiff,
  listCodecFiles,
  listProto,
  reconcileResourceWithServer,
  type BaselineSyncResult,
  type ResourceFile,
  type ResourceType,
} from './resourcesStore';

const ERRORS_JSON_NAME = 'errors.json';

export { connNameToCodecFileName } from './codecConnections';

export function codecFileNameToConnName(name: string): string {
  return codecFileNameToConnNameStrict(name);
}

/**
 * 从 flow 抽取所有 tcp/udp 动作引用的连接集合（`<proto>:<service>`），去重、排序。
 *
 * 遍历 `flow.actions`，对 pattern 以 tcp 或 udp 开头且 service 非空的动作：
 *   proto = pattern.startsWith('tcp') ? 'tcp' : 'udp'
 *   收集 `${proto}:${service}`
 * 排除无 service、非 tcp/udp（httpRequest/setState/lua 等）的动作。
 *
 * 与 refsCheck.ts:370 的 proto 推导逻辑一致（pattern 前缀决定 proto）。
 */
export function collectFlowCodecConnections(flow: FlowJson): string[] {
  const set = new Set<string>();
  for (const def of Object.values(flow.actions ?? {})) {
    if (!def?.pattern) continue;
    const p = def.pattern;
    if (!p.startsWith('tcp') && !p.startsWith('udp')) continue;
    const service = def.service?.trim();
    if (!service) continue;
    const proto = p.startsWith('tcp') ? 'tcp' : 'udp';
    set.add(`${proto}:${service}`);
  }
  return Array.from(set).sort();
}

/**
 * 给定引用连接集合与本地存储中已有的 *_codec.json 文件名，返回缺少对应文件的连接名（排序）。
 *
 * 纯函数，供单测；taskActions.startTask 内部调用。
 */
export function findMissingCodecConnections(
  referenced: string[],
  codecFileNames: string[],
): string[] {
  const have = new Set(codecFileNames);
  return referenced.filter((conn) => !have.has(connNameToCodecFileName(conn)));
}

export interface TaskResourceNames {
  scripts: string[];
  protos: string[];
  adapters: string[];
}

export async function collectTaskResourceNames(flow: FlowJson): Promise<TaskResourceNames> {
  const scripts = collectFlowScriptNames(flow);
  const protos = (await listProto()).map((f) => f.name);
  const codecs = (await listCodecFiles()).map((f) => f.name);
  const adapters = [...codecs];
  if (await getErrorMap()) adapters.push(ERRORS_JSON_NAME);
  return { scripts, protos, adapters };
}

export async function checkTaskResourcesAgainstBaseline(
  flow: FlowJson,
): Promise<BaselineSyncResult> {
  const result: BaselineSyncResult = { added: [], unchanged: [], conflicts: [], removed: [] };
  const scope = await collectTaskResourceNames(flow);
  const protoMap = new Map((await listProto()).map((f) => [f.name, f]));

  // 拉取失败（断网 / 5xx）时直接跳过该文件：reconcile 对「服务器没有 + 本地未改动」
  // 的处理是删除本地副本，把一次抖动当成删除信号会让用户的资源凭空消失。
  // 只有服务器明确答复 404 才继续走对账。
  const reconcileOne = async (
    type: ResourceType,
    name: string,
    local: ResourceFile,
  ): Promise<void> => {
    const baseline = await fetchBaselineResource(type, name);
    if (baseline.status === 'error') return;
    const serverContent = baseline.status === 'ok' ? baseline.content : null;
    await reconcileResourceWithServer(result, type, name, local, serverContent);
  };

  await Promise.all([
    ...scope.scripts.map(async (name) => {
      const local = await getScript(name);
      if (!local) return;
      await reconcileOne('script', name, local);
    }),
    ...scope.protos.map(async (name) => {
      const local = protoMap.get(name);
      if (!local) return;
      await reconcileOne('proto', name, local);
    }),
    ...scope.adapters.map(async (name) => {
      const local = name === ERRORS_JSON_NAME ? await getErrorMap() : await getCodecSchema(name);
      if (!local) return;
      await reconcileOne('adapter', name, local);
    }),
  ]);

  result.unchanged.sort(sortEntry);
  result.conflicts.sort(sortDiff);
  result.removed.sort(sortDiff);
  return result;
}

export function hasTaskResourceDiff(result: BaselineSyncResult): boolean {
  return hasSyncDiff(result);
}

function sortEntry(
  a: { type: ResourceType; name: string },
  b: { type: ResourceType; name: string },
): number {
  return `${a.type}:${a.name}`.localeCompare(`${b.type}:${b.name}`);
}

function sortDiff(
  a: { type: ResourceType; name: string },
  b: { type: ResourceType; name: string },
): number {
  return sortEntry(a, b);
}
