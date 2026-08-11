import { useFlowStore } from './flowStore';

export function undo(): boolean {
  const history = useFlowStore.temporal.getState();
  if (history.pastStates.length === 0) return false;
  history.undo();
  useFlowStore.getState().syncDerived();
  return true;
}

export function redo(): boolean {
  const history = useFlowStore.temporal.getState();
  if (history.futureStates.length === 0) return false;
  history.redo();
  useFlowStore.getState().syncDerived();
  return true;
}

export function getHistorySize(): { past: number; future: number } {
  const history = useFlowStore.temporal.getState();
  return {
    past: history.pastStates.length,
    future: history.futureStates.length,
  };
}
