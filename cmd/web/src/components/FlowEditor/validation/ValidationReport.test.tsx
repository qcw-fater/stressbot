import { App as AntApp } from 'antd';
import { render, screen } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useValidationStore } from './validationStore';
import { ValidationReportDrawer } from './ValidationReport';

vi.mock('../panels/FloatingWindow', () => ({
  FloatingWindow: ({
    open,
    extra,
    children,
  }: {
    open: boolean;
    extra?: ReactNode;
    children: ReactNode;
  }) =>
    open ? (
      <section>
        {extra}
        {children}
      </section>
    ) : null,
}));

afterEach(() => {
  useValidationStore.setState({
    report: { errors: [], warnings: [], infos: [], total: 0 },
    issuesByNodeId: {},
    context: {},
  });
});

describe('ValidationReportDrawer', () => {
  it('shows automatic-update state without exposing a no-op button', () => {
    render(
      <AntApp>
        <ValidationReportDrawer open onClose={vi.fn()} />
      </AntApp>,
    );

    const indicator = screen.getByText('自动更新');
    expect(indicator.closest('button')).toBeNull();
  });
});
