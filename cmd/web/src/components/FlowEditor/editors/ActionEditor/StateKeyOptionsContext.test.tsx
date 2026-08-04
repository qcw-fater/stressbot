import { describe, expect, it, beforeEach, vi } from 'vitest';
import { act, render, waitFor } from '@testing-library/react';
import * as React from 'react';
import { useStateKeyOptions, StateKeyOptionsProvider } from './useStateKeyOptions';
import { useFlowStore } from '../../store/flowStore';

// 必须用提升（hoisted）的 vi.mock 拦截 resourcesStore.getScript —— useStateKeyOptions
// 在模块顶层静态 import getScript，vi.doMock 对已加载的 ESM 模块无效。
const getScriptMock = vi.fn();
let resourceListener: (() => void) | undefined;
const subscribeMock = vi.fn((listener: () => void) => {
  resourceListener = listener;
  return () => {
    if (resourceListener === listener) resourceListener = undefined;
  };
});
vi.mock('@/services/resourcesStore', () => ({
  getScript: (...args: unknown[]) => getScriptMock(...args),
  subscribe: (listener: () => void) => subscribeMock(listener),
}));

// 造一个引用脚本 'demo.lua' 的 lua action，使 collectUsedScriptNames 返回 { 'demo.lua' }。
function seedScriptAction() {
  useFlowStore.getState().loadFromTaskFlow({
    defaultDelayMs: 1000,
    nodes: { main: { type: 'sequence', next: ['act1'] }, act1: { type: 'action', action: 'A1' } },
    actions: { A1: { pattern: 'lua', script: 'demo.lua' } },
    listens: {},
  });
}

const Consumer = React.forwardRef<{ keys: unknown[]; ready: boolean }>((_, ref) => {
  const r = useStateKeyOptions();
  React.useImperativeHandle(ref, () => r, [r]);
  return null;
});
Consumer.displayName = 'Consumer';

describe('StateKeyOptionsProvider', () => {
  beforeEach(() => {
    getScriptMock.mockReset();
    getScriptMock.mockResolvedValue({ name: 'demo.lua', content: 'robot.set("demoKey", 1)' });
    subscribeMock.mockClear();
    resourceListener = undefined;
    seedScriptAction();
  });

  it('provider 下多个消费方只触发一次脚本加载（去重）', async () => {
    const refA = React.createRef<{ keys: unknown[]; ready: boolean }>();
    const refB = React.createRef<{ keys: unknown[]; ready: boolean }>();
    render(
      <StateKeyOptionsProvider>
        <Consumer ref={refA} />
        <Consumer ref={refB} />
      </StateKeyOptionsProvider>,
    );

    await waitFor(() => expect(refA.current?.ready).toBe(true));
    await waitFor(() => expect(refB.current?.ready).toBe(true));

    // 1 个脚本名 → getScript 恰好调用 1 次（去重生效）；无 provider 两消费方会是 2 次。
    expect(getScriptMock).toHaveBeenCalledTimes(1);
  });

  it('无 provider 时回退到自行加载，仍能拿到 keys/ready', async () => {
    const ref = React.createRef<{ keys: unknown[]; ready: boolean }>();
    render(<Consumer ref={ref} />);

    await waitFor(() => expect(ref.current?.ready).toBe(true));
    expect(getScriptMock).toHaveBeenCalledTimes(1); // 单消费方回退也加载一次
    expect(ref.current?.keys.map((k) => (k as { key: string }).key)).toContain('demoKey');
  });

  it('资源拉取后即使脚本引用名不变也重新加载状态键', async () => {
    getScriptMock.mockResolvedValue({ name: 'demo.lua', content: '-- old content' });
    const ref = React.createRef<{ keys: unknown[]; ready: boolean }>();
    render(
      <StateKeyOptionsProvider>
        <Consumer ref={ref} />
      </StateKeyOptionsProvider>,
    );

    await waitFor(() => expect(ref.current?.ready).toBe(true));
    expect(ref.current?.keys.map((k) => (k as { key: string }).key)).not.toContain('pulledKey');
    expect(subscribeMock).toHaveBeenCalledOnce();

    getScriptMock.mockResolvedValue({ name: 'demo.lua', content: 'robot.set("pulledKey", 1)' });
    await act(async () => resourceListener?.());

    await waitFor(() => {
      expect(ref.current?.keys.map((k) => (k as { key: string }).key)).toContain('pulledKey');
    });
  });

  it('动作普通字段变化不重新加载脚本，脚本引用变化时才加载', async () => {
    const ref = React.createRef<{ keys: unknown[]; ready: boolean }>();
    render(
      <StateKeyOptionsProvider>
        <Consumer ref={ref} />
      </StateKeyOptionsProvider>,
    );
    await waitFor(() => expect(ref.current?.ready).toBe(true));
    expect(getScriptMock).toHaveBeenCalledTimes(1);
    getScriptMock.mockClear();

    await act(async () => {
      useFlowStore.getState().updateAction('A1', { timeout: 1500 });
      await Promise.resolve();
    });
    expect(getScriptMock).not.toHaveBeenCalled();

    await act(async () => {
      useFlowStore.getState().updateAction('A1', { script: 'other.lua' });
    });
    await waitFor(() => expect(getScriptMock).toHaveBeenCalledWith('other.lua'));
  });
});
