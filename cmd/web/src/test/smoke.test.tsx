import { render, screen } from '@testing-library/react';
import { Button } from 'antd';
import { describe, expect, it } from 'vitest';

describe('component test environment', () => {
  it('renders Ant Design controls in jsdom', () => {
    render(<Button>状态编辑器</Button>);
    expect(screen.getByRole('button', { name: '状态编辑器' })).toBeTruthy();
  });
});
