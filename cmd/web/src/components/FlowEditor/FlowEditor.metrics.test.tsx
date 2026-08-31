import { act, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ActionMetric } from '@/types/api';
import { useMetricsStore } from './nodes/shared/MetricsBadge';
import { FlowEditor } from './index';

vi.mock('./FlowCanvas', () => ({ FlowCanvas: () => null }));
vi.mock('./panels/Toolbar', () => ({ Toolbar: () => null }));
vi.mock('./panels/NodePalette', () => ({ NodePalette: () => null }));
vi.mock('./preview/JsonPreviewModal', () => ({ JsonPreviewModal: () => null }));
vi.mock('./editors/NodeEditorDrawer', () => ({ NodeEditorDrawer: () => null }));
vi.mock('./proto/ProtoBrowser', () => ({ ProtoBrowser: () => null }));
vi.mock('./listens/ListenPanel', () => ({ ListenPanel: () => null }));
vi.mock('./validation/ValidationReport', () => ({ ValidationReportDrawer: () => null }));
vi.mock('./library/TemplateEditorDrawer', () => ({ TemplateEditorDrawer: () => null }));
vi.mock('./validation/FlowValidationCoordinator', () => ({
  FlowValidationCoordinator: () => null,
}));
vi.mock('./editors/ActionEditor/useStateKeyOptions', () => ({
  StateKeyOptionsProvider: ({ children }: { children: React.ReactNode }) => children,
}));
vi.mock('./store/persistDraft', () => ({
  startAutoPersist: () => () => undefined,
  loadDraft: () => null,
}));
vi.mock('./proto/protoStore', () => ({
  useProtoStore: (selector: (state: { load: () => Promise<void> }) => unknown) =>
    selector({ load: async () => undefined }),
}));
vi.mock('./listens/routeKeyResolver', () => ({
  refreshRouteKeyTemplates: async () => undefined,
}));
vi.mock('@/services/resourcesStore', () => ({
  collectCodecSchemaErrors: async () => [],
  subscribe: () => () => undefined,
}));
vi.mock('@/services/baselineApi', () => ({ fetchBaselineFlow: async () => null }));

afterEach(() => {
  useMetricsStore.getState().setMetrics(undefined);
});

describe('FlowEditor metrics bridge', () => {
  it('publishes one metrics-store update when a new snapshot arrives', () => {
    const first = new Map<string, ActionMetric>();
    const second = new Map<string, ActionMetric>();
    const listener = vi.fn();
    const unsubscribe = useMetricsStore.subscribe(listener);
    const view = render(<FlowEditor autoLoadDefault={false} metrics={first} />);
    listener.mockClear();

    act(() => view.rerender(<FlowEditor autoLoadDefault={false} metrics={second} />));

    expect(listener).toHaveBeenCalledTimes(1);
    expect(useMetricsStore.getState().metrics).toBe(second);
    unsubscribe();
  });
});
