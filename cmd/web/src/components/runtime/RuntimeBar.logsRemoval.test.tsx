import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App as AntApp } from 'antd';
import { describe, expect, it } from 'vitest';
import { RuntimeBar } from './RuntimeBar';

describe('RuntimeBar log viewer removal', () => {
  it('does not expose an in-app log viewer entry', () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <AntApp>
          <RuntimeBar />
        </AntApp>
      </QueryClientProvider>,
    );

    expect(screen.queryByRole('button', { name: /日志/ })).toBeNull();
  });
});
