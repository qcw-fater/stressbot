import { Col, Form, Input, InputNumber, Row, Typography } from 'antd';
import type { RobotConfig } from '@/types/api';

interface TaskStartCommonFieldsProps {
  totalBots: number;
  /** 可分配容量（仅空闲节点，口径见 services/taskCapacity.ts） */
  capacity: number;
  rampUpEnabled: boolean;
  rampUpSum: number;
  robotConfig: RobotConfig;
  onTotalBotsChange: (value: number) => void;
  onRobotConfigChange: (patch: Partial<RobotConfig>) => void;
}

export function TaskStartCommonFields({
  totalBots,
  capacity,
  rampUpEnabled,
  rampUpSum,
  robotConfig,
  onTotalBotsChange,
  onRobotConfigChange,
}: TaskStartCommonFieldsProps) {
  const capacityHint = `可分配容量 ${capacity}（仅空闲节点）`;
  return (
    <>
      <Row gutter={16} data-testid="task-start-scale-row">
        <Col xs={24} sm={12}>
          {!rampUpEnabled ? (
            <Form.Item label="机器人数" required extra={capacityHint}>
              <InputNumber
                min={1}
                max={capacity}
                value={totalBots}
                onChange={(value) => onTotalBotsChange(typeof value === 'number' ? value : 0)}
                style={{ width: '100%' }}
              />
            </Form.Item>
          ) : (
            <Form.Item label="机器人数" extra={capacityHint}>
              <Typography.Text strong>{rampUpSum}</Typography.Text>
              <Typography.Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>
                由各阶段机器人数自动累加
              </Typography.Text>
            </Form.Item>
          )}
        </Col>
        <Col xs={24} sm={12}>
          <Form.Item label="并发" extra="每秒新建机器人数">
            <InputNumber
              min={1}
              value={robotConfig.concurrency}
              onChange={(value) =>
                onRobotConfigChange({ concurrency: typeof value === 'number' ? value : 1 })
              }
              style={{ width: '100%' }}
            />
          </Form.Item>
        </Col>
      </Row>

      <Row gutter={16} data-testid="task-start-account-row">
        <Col xs={24} sm={12}>
          <Form.Item label="账号前缀" extra="默认 bot_">
            <Input
              value={robotConfig.accountPrefix ?? ''}
              onChange={(event) => onRobotConfigChange({ accountPrefix: event.target.value })}
              placeholder="bot_"
            />
          </Form.Item>
        </Col>
        <Col xs={24} sm={12}>
          <Form.Item label="起始编号" extra="默认 0">
            <InputNumber
              min={0}
              max={1_000_000}
              value={robotConfig.startNumber ?? 0}
              onChange={(value) =>
                onRobotConfigChange({ startNumber: typeof value === 'number' ? value : 0 })
              }
              style={{ width: '100%' }}
            />
          </Form.Item>
        </Col>
      </Row>
    </>
  );
}
