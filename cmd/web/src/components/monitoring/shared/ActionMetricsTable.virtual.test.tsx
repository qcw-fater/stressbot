import { render } from '@testing-library/react';
import type { ReactElement } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ActionMetricsTable } from './ActionMetricsTable';

const mocks = vi.hoisted(() => ({
  tableProps: vi.fn(),
}));

vi.mock('antd', async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return {
    ...actual,
    Table: (props: Record<string, unknown>): ReactElement => {
      mocks.tableProps(props);
      return <div data-testid="action-table" />;
    },
  };
});

describe('ActionMetricsTable virtual scrolling', () => {
  beforeEach(() => {
    mocks.tableProps.mockClear();
  });

  it('passes numeric scroll dimensions to the virtual table', () => {
    render(<ActionMetricsTable rows={[]} scrollY={320} showToolbar={false} />);

    const props = mocks.tableProps.mock.calls.at(-1)?.[0] as {
      virtual?: boolean;
      scroll?: { x?: unknown; y?: unknown };
    };
    expect(props.virtual).toBe(true);
    expect(typeof props.scroll?.x).toBe('number');
    expect(props.scroll?.x).toBeGreaterThan(0);
    expect(props.scroll?.y).toBe(320);
  });

  it('does not enable virtual scrolling without a numeric viewport height', () => {
    render(<ActionMetricsTable rows={[]} showToolbar={false} />);

    const props = mocks.tableProps.mock.calls.at(-1)?.[0] as {
      virtual?: boolean;
      scroll?: { x?: unknown; y?: unknown };
    };
    expect(props.virtual).toBe(false);
    expect(typeof props.scroll?.x).toBe('number');
    expect(props.scroll).not.toHaveProperty('y');
  });
});
