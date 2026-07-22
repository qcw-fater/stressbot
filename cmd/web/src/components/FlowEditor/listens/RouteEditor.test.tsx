import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { RouteEditor } from './RouteEditor';

describe('RouteEditor inline layout', () => {
  it('把 route 模板字段渲染为同一行的前缀输入框', () => {
    const { container } = render(
      <RouteEditor
        layout="inline"
        value={{ cmd: 12, act: 3 }}
        server="tcp:logic"
        routeKeyTemplate="{cmd}:{act}"
        onChange={vi.fn()}
      />,
    );

    expect(screen.getByLabelText('route cmd')).toBeTruthy();
    expect(screen.getByLabelText('route act')).toBeTruthy();
    expect(container.querySelector('.ant-alert')).toBeNull();
    expect(container.querySelector('.ant-input-group-addon')).toBeNull();
  });
});
