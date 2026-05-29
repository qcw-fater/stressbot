import type { FlowJson } from '@/components/FlowEditor/codec/flowToJson';
import { fetchBaselineAdapter, fetchBaselineErrorMap, fetchBaselineProtoContent, fetchBaselineScript } from './baselineApi';
import { collectFlowScriptNames } from './scriptSync';
import {
  getAdapterScript,
  getErrorMapScript,
  getScript,
  hasSyncDiff,
  listProto,
  type BaselineSyncResult,
  type ResourceFile,
  type ResourceType,
} from './resourcesStore';

export interface TaskResourceNames {
  scripts: string[];
  protos: string[];
  adapters: string[];
}

export async function collectTaskResourceNames(flow: FlowJson): Promise<TaskResourceNames> {
  const scripts = collectFlowScriptNames(flow);
  const protos = (await listProto()).map((f) => f.name);
  const adapters = ['codec.lua'];
  const errorMap = await getErrorMapScript();
  if (errorMap) adapters.push('error.lua');
  return { scripts, protos, adapters };
}

export async function checkTaskResourcesAgainstBaseline(flow: FlowJson): Promise<BaselineSyncResult> {
  const result: BaselineSyncResult = { added: [], unchanged: [], conflicts: [], removed: [] };
  const scope = await collectTaskResourceNames(flow);
  const protoMap = new Map((await listProto()).map((f) => [f.name, f]));

  await Promise.all([
    ...scope.scripts.map(async (name) => {
      const local = await getScript(name);
      if (!local) return;
      const baseline = await fetchBaselineScript(name);
      compareText(result, 'script', name, local, baseline);
    }),
    ...scope.protos.map(async (name) => {
      const local = protoMap.get(name);
      if (!local) return;
      const baseline = await fetchBaselineProtoContent(name);
      compareText(result, 'proto', name, local, baseline);
    }),
    ...scope.adapters.map(async (name) => {
      const local = name === 'error.lua' ? await getErrorMapScript() : await getAdapterScript();
      if (!local) return;
      const baseline = name === 'error.lua' ? await fetchBaselineErrorMap() : await fetchBaselineAdapter();
      compareText(result, 'adapter', name, local, baseline);
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

function compareText(
  result: BaselineSyncResult,
  type: ResourceType,
  name: string,
  local: ResourceFile,
  baseline: string | null,
): void {
  if (baseline === null) {
    result.removed.push({ type, name, localContent: local.content, baselineContent: '' });
    return;
  }
  if (local.content === baseline) {
    result.unchanged.push({ type, name });
    return;
  }
  result.conflicts.push({ type, name, localContent: local.content, baselineContent: baseline });
}

function sortEntry(a: { type: ResourceType; name: string }, b: { type: ResourceType; name: string }): number {
  return `${a.type}:${a.name}`.localeCompare(`${b.type}:${b.name}`);
}

function sortDiff(a: { type: ResourceType; name: string }, b: { type: ResourceType; name: string }): number {
  return sortEntry(a, b);
}
