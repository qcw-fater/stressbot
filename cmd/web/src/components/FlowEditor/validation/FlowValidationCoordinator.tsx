import { useEffect, useRef } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';
import { useProtoStore } from '../proto/protoStore';
import { useStateKeyOptions } from '../editors/ActionEditor/useStateKeyOptions';
import { createValidationScheduler, validateAndPublish, type ValidationScheduler } from './validationStore';

/** 统一流程校验入口：连续编辑时只在停止输入 150ms 后校验一次。 */
export function FlowValidationCoordinator() {
  const flow = useFlowStore(
    useShallow((state) => ({
      defaultDelayMs: state.defaultDelayMs,
      nodes: state.nodes,
      actions: state.actions,
      listens: state.listens,
    })),
  );
  const routeKeyTemplatesVersion = useEditorStore((state) => state.routeKeyTemplatesVersion);
  // proto 注册表是异步加载的，且 validateFlow 直接读 protoRegistry 单例。必须把 proto
  // 加载状态/内容版本纳入依赖，否则 proto 加载完成时不会触发重新校验——首帧校验与 proto
  // 加载纯竞态，导致 proto 相关检查（C2S/S2C 查找、字段路径）偶现漏检或基于残缺注册表误报。
  const protoStatus = useProtoStore((s) => s.status);
  const protoHash = useProtoStore((s) => s.hash);
  const { keys: stateKeys, ready: stateKeysReady } = useStateKeyOptions();
  const schedulerRef = useRef<ValidationScheduler | null>(null);
  if (schedulerRef.current === null) {
    schedulerRef.current = createValidationScheduler(validateAndPublish, 150);
  }

  useEffect(() => {
    const scheduler = schedulerRef.current;
    if (!scheduler) return;
    scheduler.schedule(flow, { stateKeys, stateKeysReady });
    return () => scheduler.cancel();
  }, [flow, routeKeyTemplatesVersion, stateKeys, stateKeysReady, protoStatus, protoHash]);

  return null;
}
