/**
 * useStateKeyOptions —— 状态候选数据源的统一异步加载钩子。
 *
 * 此前 StateKeyInput 与 shared/StateExprInput 各自重复实现「从 flow graph 推导需引用的
 * 脚本名 → 异步拉取脚本内容 → collectStateKeys」的逻辑。本钩子将其收敛为一处，
 * 两个消费方共享同一份（当前 bindings 由参数区分）。
 *
 * ready：脚本加载完成（或无需加载脚本）后置 true，调用方可据此显示骨架/占位。
 */

import { useEffect, useMemo, useState } from 'react';
import type { FieldBind } from '@/types/action';
import { useRuntimeStore } from '@/services/runtimeStore';
import { getScript } from '@/services/resourcesStore';
import { useFlowStore } from '../../store/flowStore';
import { collectStateKeys, collectUsedScriptNames, type StateKeyInfo } from './stateRegistry';

export interface StateKeyOptionsResult {
  keys: StateKeyInfo[];
  ready: boolean;
}

export function useStateKeyOptions(currentBindings?: FieldBind[]): StateKeyOptionsResult {
  const actions = useFlowStore((s) => s.actions);
  const listens = useFlowStore((s) => s.listens);
  const nodes = useFlowStore((s) => s.nodes);
  const stateExtra = useRuntimeStore((s) => s.robotConfig.stateExtra);
  const scriptNames = useMemo(
    () => collectUsedScriptNames(actions, listens, nodes),
    [actions, listens, nodes],
  );
  const [scripts, setScripts] = useState<Array<{ name: string; content: string }>>([]);
  const [ready, setReady] = useState(scriptNames.size === 0);

  useEffect(() => {
    let cancelled = false;
    setReady(scriptNames.size === 0);
    if (scriptNames.size === 0) {
      setScripts([]);
      return () => { cancelled = true; };
    }
    Promise.all([...scriptNames].map(async (name) => {
      try {
        const file = await getScript(name);
        return file ? { name: file.name, content: file.content } : null;
      } catch {
        return null;
      }
    })).then((loaded) => {
      if (cancelled) return;
      setScripts(loaded.filter((it): it is { name: string; content: string } => it !== null));
      setReady(true);
    });
    return () => { cancelled = true; };
  }, [scriptNames]);

  const keys = useMemo(
    () => collectStateKeys(actions, listens, stateExtra, currentBindings, scripts),
    [actions, listens, stateExtra, currentBindings, scripts],
  );
  return { keys, ready };
}
