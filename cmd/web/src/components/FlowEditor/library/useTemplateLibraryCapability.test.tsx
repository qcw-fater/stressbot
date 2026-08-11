import type { PropsWithChildren } from 'react';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { getCapabilities } from '@/services/capabilitiesApi';
import { useTemplateLibraryCapability } from './useTemplateLibraryCapability';
import { createTestQueryClient } from '@/services/queryClient';

vi.mock('@/services/capabilitiesApi', () => ({ getCapabilities: vi.fn() }));

const getCapabilitiesMock = vi.mocked(getCapabilities);

beforeEach(() => vi.clearAllMocks());

function makeWrapper() {
  const client = createTestQueryClient();
  return function Wrapper({ children }: PropsWithChildren) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  };
}

describe('useTemplateLibraryCapability', () => {
  it('从加载态进入禁用态', async () => {
    let resolve!: (value: {
      sharedState: boolean;
      flowLibrary: boolean;
      templateLibrary: boolean;
    }) => void;
    getCapabilitiesMock.mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    const { result } = renderHook(() => useTemplateLibraryCapability(), { wrapper: makeWrapper() });

    expect(result.current).toMatchObject({ loading: true, templateLibrary: undefined });
    act(() => resolve({ sharedState: false, flowLibrary: false, templateLibrary: false }));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.templateLibrary).toBe(false);
  });

  it('支持手动重试，并在刷新失败时保留最近一次可用状态', async () => {
    getCapabilitiesMock
      .mockResolvedValueOnce({ sharedState: false, flowLibrary: true, templateLibrary: true })
      .mockRejectedValueOnce(new Error('暂时无法连接'))
      .mockResolvedValueOnce({ sharedState: false, flowLibrary: true, templateLibrary: false });
    const { result } = renderHook(() => useTemplateLibraryCapability(), { wrapper: makeWrapper() });
    await waitFor(() => expect(result.current.templateLibrary).toBe(true));

    await act(async () => {
      await result.current.refresh();
    });
    expect(result.current.templateLibrary).toBe(true);
    await waitFor(() => expect(result.current.error?.message).toBe('暂时无法连接'));

    await act(async () => {
      await result.current.refresh();
    });
    await waitFor(() => expect(result.current.templateLibrary).toBe(false));
    expect(result.current.error).toBeUndefined();
  });

  it('窗口重新获得焦点时刷新能力状态', async () => {
    getCapabilitiesMock
      .mockResolvedValueOnce({ sharedState: false, flowLibrary: true, templateLibrary: true })
      .mockResolvedValueOnce({ sharedState: false, flowLibrary: true, templateLibrary: false });
    const { result } = renderHook(() => useTemplateLibraryCapability(), { wrapper: makeWrapper() });
    await waitFor(() => expect(result.current.templateLibrary).toBe(true));

    act(() => window.dispatchEvent(new Event('focus')));
    await waitFor(() => expect(result.current.templateLibrary).toBe(false));
    expect(getCapabilitiesMock).toHaveBeenCalledTimes(2);
  });
});
