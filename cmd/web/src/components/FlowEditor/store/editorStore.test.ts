import { beforeEach, describe, expect, it } from 'vitest';
import type { ActivePanel } from './editorStore';
import { useEditorStore } from './editorStore';
import { useFloatingWindowStore } from './floatingWindowStore';

describe('editorStore panel sizes', () => {
  beforeEach(() => {
    useEditorStore.setState({ activePanel: {} });
    useFloatingWindowStore.setState({ windows: {}, _nextZ: 1000 });
  });

  it.each<{
    panel: ActivePanel;
    windowId: 'nodeEdit' | 'listenEdit';
  }>([
    { panel: { kind: 'nodeEdit', nodeId: 'node-a' }, windowId: 'nodeEdit' },
    { panel: { kind: 'listenEdit', listenName: 'listen-a' }, windowId: 'listenEdit' },
  ])('opens $windowId at the shared editor width', ({ panel, windowId }) => {
    useEditorStore.getState().setActivePanel(panel);

    expect(useFloatingWindowStore.getState().windows[windowId]?.size.width).toBe(720);
  });
});
