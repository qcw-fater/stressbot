import { useEffect, useRef } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';
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
  }, [flow, routeKeyTemplatesVersion, stateKeys, stateKeysReady]);

  return null;
}
