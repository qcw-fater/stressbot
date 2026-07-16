import { create } from 'zustand';
import type { TaskFlow } from '@/types/flow';
import { useFlowStore } from '../store/flowStore';
import {
  validateFlow,
  type FlowValidationContext,
  type ValidationIssue,
  type ValidationReport,
} from './refsCheck';

const EMPTY_REPORT: ValidationReport = {
  errors: [],
  warnings: [],
  infos: [],
  total: 0,
};

interface ValidationState {
  report: ValidationReport;
  issuesByNodeId: Record<string, ValidationIssue[]>;
  context: FlowValidationContext;
}

export const useValidationStore = create<ValidationState>()(() => ({
  report: EMPTY_REPORT,
  issuesByNodeId: {},
  context: {},
}));

export function validateAndPublish(
  flow: TaskFlow,
  context: FlowValidationContext,
): ValidationReport {
  const report = validateFlow(flow, context);
  const issuesByNodeId = groupIssuesByNode(report, flow);
  useValidationStore.setState({ report, issuesByNodeId, context });
  return report;
}

/** 强一致操作使用最新上下文同步校验，不依赖尚未触发的防抖结果。 */
export function validateLatestFlow(flow: TaskFlow = useFlowStore.getState().toTaskFlow()): ValidationReport {
  return validateAndPublish(flow, useValidationStore.getState().context);
}

export function groupIssuesByNode(
  report: ValidationReport,
  flow: Pick<TaskFlow, 'nodes'>,
): Record<string, ValidationIssue[]> {
  const grouped: Record<string, ValidationIssue[]> = {};
  for (const issue of [...report.errors, ...report.warnings]) {
    const location = issue.location;
    if (!location) continue;
    if (location.kind === 'node') {
      (grouped[location.id] ??= []).push(issue);
      continue;
    }
    if (location.kind === 'listen') {
      (grouped[`__cb__${location.id}`] ??= []).push(issue);
      continue;
    }
    for (const [nodeId, node] of Object.entries(flow.nodes)) {
      if (node.type === 'action' && node.action === location.id) {
        (grouped[nodeId] ??= []).push(issue);
      }
    }
  }
  return grouped;
}

export interface ValidationScheduler {
  schedule: (flow: TaskFlow, context: FlowValidationContext) => void;
  flush: (flow: TaskFlow, context: FlowValidationContext) => void;
  cancel: () => void;
}

export function createValidationScheduler(
  run: (flow: TaskFlow, context: FlowValidationContext) => void,
  delayMs = 150,
): ValidationScheduler {
  let timer: ReturnType<typeof setTimeout> | null = null;

  const cancel = () => {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  };

  return {
    schedule(flow, context) {
      cancel();
      timer = setTimeout(() => {
        timer = null;
        run(flow, context);
      }, delayMs);
    },
    flush(flow, context) {
      cancel();
      run(flow, context);
    },
    cancel,
  };
}
