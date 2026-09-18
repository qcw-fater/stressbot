/**
 * useStateKeyOptions —— 状态候选数据源的统一异步加载钩子 + 共享 Provider。
 *
 * 成本集中在「异步拉取流程引用的 Lua 脚本」（getScript IDB 读）；
 * collectStateKeys 本身是廉价同步扫描。StateKeyOptionsProvider 在 FlowEditor
 * 顶层挂载一次，加载脚本并经 context 共享 { scripts, ready }；消费方经
 * useStateKeyOptions(currentBindings?) 读取：在 Provider 下复用共享脚本，
 * 无 Provider 时回退到自行加载（保持独立可用 / 测试友好）。
 *
 * ready：脚本加载完成（或无需加载脚本）后置 true，调用方可据此显示骨架/占位。
 */

import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useShallow } from 'zustand/react/shallow';
import type { FieldBind } from '@/types/action';
import { useRuntimeStore } from '@/services/runtimeStore';
import { getScript, subscribe } from '@/services/resourcesStore';
import { useFlowStore } from '../../store/flowStore';
import { collectStateKeys, collectUsedScriptNames, type StateKeyInfo } from './stateRegistry';

type LuaScript = { name: string; content: string };

export interface StateKeyOptionsResult {
  keys: StateKeyInfo[];
  ready: boolean;
  /** flow 引用、但本地资源里没有的脚本名（仅 ready 后有意义） */
  missingScripts: string[];
}

interface LoadedScripts {
  scripts: LuaScript[];
  ready: boolean;
  missing: string[];
}

interface SharedStateKeys {
  keys: StateKeyInfo[];
  ready: boolean;
  missing: string[];
}

const EMPTY_ACTIONS = {};
const EMPTY_LISTENS = {};
const EMPTY_NODES = {};
const EMPTY_STATE_EXTRA = {};

const StateKeyOptionsContext = createContext<SharedStateKeys | null>(null);

/**
 * 异步加载流程引用的 Lua 脚本。enabled=false 时不加载（Provider 已加载时由
 * 消费方短路），保证遵守 Rules of Hooks（始终调用本钩子），同时在 Provider 下零重复加载。
 */
function useLoadedStateKeyScripts(enabled: boolean): LoadedScripts {
  const [resourceVersion, setResourceVersion] = useState(0);
  useEffect(() => {
    if (!enabled) return;
    return subscribe(() => setResourceVersion((version) => version + 1));
  }, [enabled]);

  const scriptSources = useFlowStore(
    useShallow((state) => ({
      actions: enabled ? state.actions : EMPTY_ACTIONS,
      listens: enabled ? state.listens : EMPTY_LISTENS,
      nodes: enabled ? state.nodes : EMPTY_NODES,
    })),
  );
  const scriptNameSignature = useMemo(() => {
    if (!enabled) return '';
    return [
      ...collectUsedScriptNames(scriptSources.actions, scriptSources.listens, scriptSources.nodes),
    ]
      .sort()
      .join('\u0000');
  }, [enabled, scriptSources.actions, scriptSources.listens, scriptSources.nodes]);
  const scriptNames = useMemo(
    () => (scriptNameSignature ? scriptNameSignature.split('\u0000') : []),
    [scriptNameSignature],
  );
  const [scripts, setScripts] = useState<LuaScript[]>([]);
  const [missing, setMissing] = useState<string[]>([]);
  const [ready, setReady] = useState(!enabled || scriptNames.length === 0);

  useEffect(() => {
    if (!enabled) {
      setScripts([]);
      setMissing([]);
      setReady(true);
      return;
    }
    let cancelled = false;
    setReady(scriptNames.length === 0);
    if (scriptNames.length === 0) {
      setScripts([]);
      setMissing([]);
      return () => {
        cancelled = true;
      };
    }
    Promise.all(
      scriptNames.map(async (name) => {
        try {
          const file = await getScript(name);
          // 读到了 undefined 是「本地确实没有这个脚本」，抛异常是「本地存储读失败」。
          // 两者必须分开：后者报成「脚本缺失」会让用户去补一个其实存在的文件。
          return file ? { script: { name: file.name, content: file.content } } : { absent: name };
        } catch {
          return {};
        }
      }),
    ).then((loaded) => {
      if (cancelled) return;
      setScripts(loaded.map((it) => it.script).filter((it): it is LuaScript => it !== undefined));
      setMissing(loaded.map((it) => it.absent).filter((it): it is string => it !== undefined));
      setReady(true);
    });
    return () => {
      cancelled = true;
    };
  }, [enabled, scriptNameSignature, scriptNames, resourceVersion]);

  return { scripts, ready, missing };
}

/** FlowEditor 顶层挂载一次，共享脚本加载结果。 */
interface StateKeyOptionsProviderProps {
  children: ReactNode;
}

export function StateKeyOptionsProvider({ children }: StateKeyOptionsProviderProps) {
  const loaded = useLoadedStateKeyScripts(true);
  const actions = useFlowStore((state) => state.actions);
  const listens = useFlowStore((state) => state.listens);
  const stateExtra = useRuntimeStore((state) => state.robotConfig.stateExtra);
  const keys = useMemo(
    () => collectStateKeys(actions, listens, stateExtra, undefined, loaded.scripts),
    [actions, listens, stateExtra, loaded.scripts],
  );
  const value = useMemo(
    () => ({ keys, ready: loaded.ready, missing: loaded.missing }),
    [keys, loaded.ready, loaded.missing],
  );
  return (
    <StateKeyOptionsContext.Provider value={value}>{children}</StateKeyOptionsContext.Provider>
  );
}

/**
 * 读取状态候选。在 StateKeyOptionsProvider 下复用共享脚本（去重）；
 * 无 Provider 时回退到自行加载（独立可用）。currentBindings 仅影响当次 keys 合并，
 * 不触发额外脚本加载。
 */
export function useStateKeyOptions(currentBindings?: FieldBind[]): StateKeyOptionsResult {
  const ctx = useContext(StateKeyOptionsContext);
  const local = useLoadedStateKeyScripts(ctx === null);
  const ready = ctx ? ctx.ready : local.ready;
  const missingScripts = ctx ? ctx.missing : local.missing;

  const actions = useFlowStore((state) => (ctx ? EMPTY_ACTIONS : state.actions));
  const listens = useFlowStore((state) => (ctx ? EMPTY_LISTENS : state.listens));
  const stateExtra = useRuntimeStore((state) =>
    ctx ? EMPTY_STATE_EXTRA : state.robotConfig.stateExtra,
  );
  const keys = useMemo(
    () =>
      ctx
        ? mergeCurrentBindingKeys(ctx.keys, currentBindings)
        : collectStateKeys(actions, listens, stateExtra, currentBindings, local.scripts),
    [actions, listens, stateExtra, currentBindings, ctx, local.scripts],
  );
  return { keys, ready, missingScripts };
}

function mergeCurrentBindingKeys(
  baseKeys: StateKeyInfo[],
  currentBindings: FieldBind[] | undefined,
): StateKeyInfo[] {
  if (!currentBindings?.some((binding) => binding.storeAs)) return baseKeys;
  const map = new Map(baseKeys.map((info) => [info.key, info]));
  for (const binding of currentBindings) {
    if (!binding.storeAs) continue;
    const existing = map.get(binding.storeAs);
    const isFallback =
      existing?.sourceType === 'lua' ||
      ((existing?.sourceType === 'store' || existing?.sourceType === 'listenStore') &&
        !existing.storeField);
    if (!existing || isFallback) {
      map.set(binding.storeAs, {
        key: binding.storeAs,
        sourceType: 'storeAs',
        sourceName: '当前节点',
      });
    }
  }
  return [...map.values()].sort((a, b) => a.key.localeCompare(b.key));
}
