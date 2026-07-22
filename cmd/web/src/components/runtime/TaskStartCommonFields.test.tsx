import { render, within } from '@testing-library/react';
import { Form } from 'antd';
import { describe, expect, it, vi } from 'vitest';
import { TaskStartCommonFields } from './TaskStartCommonFields';

describe('TaskStartCommonFields', () => {
  it('groups fields into responsive rows with concise helper text', () => {
    const view = render(
      <Form layout="vertical">
        <TaskStartCommonFields
          totalBots={100}
          totalCapacity={500}
          rampUpEnabled={false}
          rampUpSum={0}
          robotConfig={{
            concurrency: 10,
            timeoutSec: 60,
            accountPrefix: 'bot_',
            startNumber: 0,
          }}
          onTotalBotsChange={vi.fn()}
          onRobotConfigChange={vi.fn()}
        />
      </Form>,
    );

    const scaleRow = view.getByTestId('task-start-scale-row');
    const accountRow = view.getByTestId('task-start-account-row');

    expect(within(scaleRow).getByText('机器人数')).toBeTruthy();
    expect(within(scaleRow).getByText('集群总容量 500')).toBeTruthy();
    expect(within(scaleRow).getByText('并发')).toBeTruthy();
    expect(within(scaleRow).getByText('每秒新建机器人数')).toBeTruthy();
    expect(within(accountRow).getByText('账号前缀')).toBeTruthy();
    expect(within(accountRow).getByText('默认 bot_')).toBeTruthy();
    expect(within(accountRow).getByText('起始编号')).toBeTruthy();
    expect(within(accountRow).getByText('默认 0')).toBeTruthy();
    expect(within(accountRow).queryByText(/账号格式/)).toBeNull();
    expect(within(accountRow).queryByText(/避免撞车/)).toBeNull();
    expect(scaleRow.querySelectorAll('.ant-col-sm-12')).toHaveLength(2);
    expect(accountRow.querySelectorAll('.ant-col-sm-12')).toHaveLength(2);
    expect(accountRow.querySelectorAll('.ant-col-xs-24')).toHaveLength(2);
  });
});
