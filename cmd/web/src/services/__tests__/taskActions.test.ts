import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  validateFlow: vi.fn(),
  clearMonitorData: vi.fn(),
  setMetrics: vi.fn(),
  loadFromTaskFlow: vi.fn(),
  setOwnedTaskId: vi.fn(),
  setMode: vi.fn(),
  setActiveTask: vi.fn(),
  syncFlowScriptsToIdb: vi.fn(),
  collectFlowScriptNames: vi.fn(),
  listProto: vi.fn(),
  listScript: vi.fn(),
  listCodecFiles: vi.fn(),
  getErrorMap: vi.fn(),
  markResourcesAsBaselineSynced: vi.fn(),
  collectFlowCodecConnections: vi.fn(),
  findMissingCodecConnections: vi.fn(),
  createTask: vi.fn(),
  startRemoteTask: vi.fn(),
}));

const runtimeState = {
  agents: [{ agentId: 'node-1', status: 'online', maxBots: 100 }],
  clearMonitorData: mocks.clearMonitorData,
  setOwnedTaskId: mocks.setOwnedTaskId,
  setMode: mocks.setMode,
  setActiveTask: mocks.setActiveTask,
};

vi.mock('@/components/FlowEditor/validation/refsCheck', () => ({
  validateFlow: (...args: unknown[]) => mocks.validateFlow(...args),
}));
vi.mock('@/components/FlowEditor/nodes/shared/MetricsBadge', () => ({
  useMetricsStore: { getState: () => ({ setMetrics: mocks.setMetrics }) },
}));
vi.mock('@/components/FlowEditor/store/flowStore', () => ({
  useFlowStore: {
    getState: () => ({
      nodes: {},
      layout: { nodePositions: {} },
      loadFromTaskFlow: mocks.loadFromTaskFlow,
      toTaskFlow: vi.fn(),
    }),
  },
}));
vi.mock('@/components/FlowEditor/proto/protoStore', () => ({
  useProtoStore: { getState: vi.fn() },
}));
vi.mock('../runtimeStore', () => ({
  useRuntimeStore: { getState: () => runtimeState },
}));
vi.mock('../scriptSync', () => ({
  syncFlowScriptsToIdb: (...args: unknown[]) => mocks.syncFlowScriptsToIdb(...args),
  collectFlowScriptNames: (...args: unknown[]) => mocks.collectFlowScriptNames(...args),
}));
vi.mock('../resourcesStore', () => ({
  listProto: (...args: unknown[]) => mocks.listProto(...args),
  listScript: (...args: unknown[]) => mocks.listScript(...args),
  listCodecFiles: (...args: unknown[]) => mocks.listCodecFiles(...args),
  getErrorMap: (...args: unknown[]) => mocks.getErrorMap(...args),
  markResourcesAsBaselineSynced: (...args: unknown[]) => mocks.markResourcesAsBaselineSynced(...args),
}));
vi.mock('../taskResourceDiff', () => ({
  collectFlowCodecConnections: (...args: unknown[]) => mocks.collectFlowCodecConnections(...args),
  findMissingCodecConnections: (...args: unknown[]) => mocks.findMissingCodecConnections(...args),
}));
vi.mock('../capabilitiesApi', () => ({ getCapabilities: vi.fn() }));
vi.mock('../tasksApi', () => ({
  createTask: (...args: unknown[]) => mocks.createTask(...args),
  startTask: (...args: unknown[]) => mocks.startRemoteTask(...args),
}));

import { startTask, type StartTaskOptions } from '../taskActions';

const flow = {
  defaultDelayMs: 1000,
  nodes: { main: { type: 'sequence' as const, next: [] } },
  actions: {},
  listens: {},
};

const options: StartTaskOptions = {
  name: 'test',
  totalBots: 10,
  robotConfig: {
    concurrency: 1,
    timeoutSec: 30,
    accountPrefix: 'bot_',
    startNumber: 0,
    mainService: 'logic',
    stateExtra: {},
    heartbeatSec: 5,
    httpTimeoutSec: 10,
    apdexT: 100,
    logLevel: 'info',
  },
  flow,
};

describe('startTask transaction boundary', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.validateFlow.mockReturnValue({ errors: [], warnings: [], infos: [], total: 0 });
    mocks.syncFlowScriptsToIdb.mockResolvedValue({ missing: [], synced: [] });
    mocks.collectFlowScriptNames.mockReturnValue([]);
    mocks.listProto.mockResolvedValue([]);
    mocks.listScript.mockResolvedValue([]);
    mocks.listCodecFiles.mockResolvedValue([{ name: 'tcp_logic_codec.json', content: '{}' }]);
    mocks.getErrorMap.mockResolvedValue(null);
    mocks.collectFlowCodecConnections.mockReturnValue([]);
    mocks.findMissingCodecConnections.mockReturnValue([]);
    mocks.createTask.mockResolvedValue({ id: 'task-1' });
    mocks.startRemoteTask.mockResolvedValue({});
    mocks.markResourcesAsBaselineSynced.mockResolvedValue(undefined);
  });

  it('keeps previous monitor data when resource preflight fails', async () => {
    mocks.syncFlowScriptsToIdb.mockResolvedValue({ missing: ['missing.lua'], synced: [] });

    await expect(startTask(options)).rejects.toThrow('缺少脚本');

    expect(mocks.clearMonitorData).not.toHaveBeenCalled();
    expect(mocks.setMetrics).not.toHaveBeenCalled();
    expect(mocks.loadFromTaskFlow).not.toHaveBeenCalled();
  });

  it('keeps previous monitor data when the remote start fails', async () => {
    mocks.startRemoteTask.mockRejectedValue(new Error('start failed'));

    await expect(startTask(options)).rejects.toThrow('start failed');

    expect(mocks.clearMonitorData).not.toHaveBeenCalled();
    expect(mocks.setMetrics).not.toHaveBeenCalled();
    expect(mocks.loadFromTaskFlow).not.toHaveBeenCalled();
  });

  it('clears previous data once after the remote start succeeds', async () => {
    await expect(startTask(options)).resolves.toBe('task-1');

    expect(mocks.clearMonitorData).toHaveBeenCalledTimes(1);
    expect(mocks.setMetrics).toHaveBeenCalledOnce();
    expect(mocks.setMetrics).toHaveBeenCalledWith(undefined);
    expect(mocks.loadFromTaskFlow).toHaveBeenCalledOnce();
    expect(mocks.startRemoteTask.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.clearMonitorData.mock.invocationCallOrder[0],
    );
  });
});
