/**
 * pattern 选择器：14 种 pattern 的下拉，附带说明 tooltip。
 */

import { Select, Tooltip } from 'antd';
import type { ActionPattern } from '@/types/action';

export const PATTERN_DESC: Record<ActionPattern, string> = {
  tcpSend: '通过 TCP 发送 C2S 消息（无应答）',
  tcpRequest: '通过 TCP 发送 C2S 消息并等待 S2C 应答',
  tcpConnect: '建立 TCP 连接到指定 service',
  tcpClose: '关闭 TCP 连接',
  tcpListen: '监听 TCP 推送消息（轮询 state）',
  udpSend: '通过 UDP 发送 C2S 消息（无应答）',
  udpRequest: '通过 UDP 发送 C2S 消息并等待 S2C 应答',
  udpConnect: '建立 UDP 连接',
  udpClose: '关闭 UDP 连接',
  udpListen: '监听 UDP 推送消息（轮询 state）',
  httpRequest: '发送 HTTP 请求',
  setState: '直接给 state 写入值（绑定列表）',
  clearState: '清空指定的 state key',
  lua: '执行一段 Lua 脚本（function execute(r)）',
};

export interface PatternSelectorProps {
  value: ActionPattern;
  onChange: (v: ActionPattern) => void;
}

const PATTERN_GROUPS: { label: string; patterns: ActionPattern[] }[] = [
  { label: '网络通信', patterns: ['tcpSend', 'udpSend', 'tcpRequest', 'udpRequest', 'httpRequest'] },
  { label: '连接管理', patterns: ['tcpConnect', 'udpConnect', 'tcpClose', 'udpClose'] },
  { label: '监听', patterns: ['tcpListen', 'udpListen'] },
  { label: '状态操作', patterns: ['setState', 'clearState'] },
  { label: '脚本', patterns: ['lua'] },
];

export function PatternSelector({ value, onChange }: PatternSelectorProps) {
  return (
    <Select
      value={value}
      onChange={onChange}
      style={{ width: 240 }}
      options={PATTERN_GROUPS.map((g) => ({
        label: g.label,
        options: g.patterns.map((p) => ({
          value: p,
          label: (
            <Tooltip title={PATTERN_DESC[p]} placement="right">
              <span className="pattern-badge" data-pattern={p} style={{ margin: 0, height: 20, lineHeight: '18px' }}>{p}</span>
            </Tooltip>
          ),
        })),
      }))}
    />
  );
}
