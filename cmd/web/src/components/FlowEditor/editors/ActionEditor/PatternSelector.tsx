/**
 * pattern 选择器：11 种 pattern 的下拉，附带说明 tooltip。
 */

import { Select, Tag, Tooltip } from 'antd';
import type { ActionPattern } from '@/types/action';
import { ALL_ACTION_PATTERNS } from '@/types/action';

const PATTERN_DESC: Record<ActionPattern, string> = {
  tcpSend: '通过 TCP 发送 C2S 消息（无应答）',
  tcpRequest: '通过 TCP 发送 C2S 消息并等待 S2C 应答',
  lua: '执行一段 Lua 脚本（function execute(r)）',
  connect: '建立 TCP 连接到指定 service',
  connectUDP: '建立 UDP 连接',
  exchangeKey: '与服务端交换加密密钥',
  close: '关闭某个连接（tcp / udp）',
  clearState: '清空指定的 state key',
  udpSendProto: '通过 UDP 发送 proto 消息',
  waitListen: '等待某个推送消息（轮询 state）',
  setState: '直接给 state 写入值（绑定列表）',
};

export interface PatternSelectorProps {
  value: ActionPattern;
  onChange: (v: ActionPattern) => void;
}

export function PatternSelector({ value, onChange }: PatternSelectorProps) {
  return (
    <Select
      value={value}
      onChange={onChange}
      style={{ width: 240 }}
      options={ALL_ACTION_PATTERNS.map((p) => ({
        value: p,
        label: (
          <Tooltip title={PATTERN_DESC[p]} placement="right">
            <Tag style={{ margin: 0 }}>{p}</Tag>
          </Tooltip>
        ),
      }))}
    />
  );
}
