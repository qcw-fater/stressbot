/**
 * DeclarativeForm 路由测试。
 *
 * 仅校验 pattern → 子编辑器的路由关系，不进入子编辑器内部行为：
 *   - setState 渲染 SetStateEditor，不渲染通用 BindingsTable
 *   - clearState 渲染 ClearStateEditor，不渲染自由 tags 输入
 *   - tcpSend 仍使用通用 BindingsTable
 *
 * 通过 vi.mock 把所有外部依赖（store hooks、codec hooks、ProtoBrowser、TargetConnectionRouteEditor、
 * 以及子编辑器 BindingsTable/SetStateEditor/ClearStateEditor/StoreTable）替换为携带 data-testid 的 stub，
 * 既避免触发 IndexedDB/流程图实时状态，也避免给生产组件加测试专用 ID。
 */

import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import type { ActionDef } from '@/types/action';

// --- 外部依赖 mock：全部惰性返回，避免 DeclarativeForm 在读字段时报错 ---

vi.mock('@/services/runtimeStore', () => ({
  useRuntimeStore: vi.fn(() => ({
    robotConfig: { timeoutSec: 60, stateExtra: {} },
  })),
}));

vi.mock('../../store/flowStore', () => ({
  useFlowStore: vi.fn(() => ({ actions: {}, listens: {} })),
}));

vi.mock('../../codec/useCodecConnections', () => ({
  useCodecConnections: vi.fn(() => ({
    connections: [], loading: false, error: null,
  })),
  useCodecRouteSpecs: vi.fn(() => ({
    specs: [], loading: false, error: null,
  })),
}));

vi.mock('../../proto/ProtoRegistry', () => ({
  protoRegistry: { isLoaded: () => false, lookupMessage: () => null },
}));

vi.mock('../../proto/ProtoBrowser', () => ({
  ProtoBrowser: () => null,
}));

vi.mock('../../codec/TargetConnectionRouteEditor', () => ({
  TargetConnectionRouteEditor: () => null,
}));

// --- 子编辑器 mock：每个渲染带 data-testid 的 div，作为路由断言锚点 ---

vi.mock('./BindingsTable', () => ({
  BindingsTable: () => <div data-testid="bindings-table" />,
}));

vi.mock('./StoreTable', () => ({
  StoreTable: () => <div data-testid="store-table" />,
}));

vi.mock('./SetStateEditor', () => ({
  SetStateEditor: () => <div data-testid="set-state-editor" />,
}));

vi.mock('./ClearStateEditor', () => ({
  ClearStateEditor: () => <div data-testid="clear-state-editor" />,
}));

import { DeclarativeForm } from './DeclarativeForm';

describe('DeclarativeForm pattern 路由', () => {
  it('setState 只渲染 SetStateEditor，不渲染通用 BindingsTable', () => {
    const action: ActionDef = { pattern: 'setState', bindings: [] };
    render(<DeclarativeForm action={action} onChange={vi.fn()} />);
    expect(screen.getByTestId('set-state-editor')).toBeTruthy();
    expect(screen.queryByTestId('bindings-table')).toBeNull();
  });

  it('clearState 渲染 ClearStateEditor，不渲染自由 tags 输入', () => {
    const action: ActionDef = { pattern: 'clearState', keys: [] };
    render(<DeclarativeForm action={action} onChange={vi.fn()} />);
    expect(screen.getByTestId('clear-state-editor')).toBeTruthy();
    expect(screen.queryByPlaceholderText('输入 state key，回车确认')).toBeNull();
  });

  it('tcpSend 仍使用通用 BindingsTable', () => {
    const action: ActionDef = {
      pattern: 'tcpSend',
      service: 'logic',
      route: {},
      c2sProto: 'X.Foo',
      bindings: [],
    };
    render(<DeclarativeForm action={action} onChange={vi.fn()} />);
    expect(screen.getByTestId('bindings-table')).toBeTruthy();
    expect(screen.queryByTestId('set-state-editor')).toBeNull();
  });
});
