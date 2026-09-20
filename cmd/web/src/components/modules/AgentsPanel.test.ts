import { describe, expect, it } from 'vitest';
import { endpointLabel } from './AgentsPanel';

describe('endpointLabel', () => {
  it('主机名与端口都可用时拼成 host:port', () => {
    expect(endpointLabel('stress-node-01', '10.0.0.7:51324')).toBe('stress-node-01:51324');
  });

  it('IPv6 地址取最后一段作为端口', () => {
    expect(endpointLabel('stress-node-01', '[::1]:51324')).toBe('stress-node-01:51324');
  });

  it('地址为空时只显示主机名，不留下半截冒号', () => {
    expect(endpointLabel('stress-node-01', '')).toBe('stress-node-01');
  });

  it('地址不含端口时只显示主机名', () => {
    expect(endpointLabel('stress-node-01', 'bufconn')).toBe('stress-node-01');
  });

  it('主机名与端口都缺失时给出占位符', () => {
    expect(endpointLabel('', '')).toBe('—');
  });

  it('主机名缺失但有端口时退化为只显示端口', () => {
    expect(endpointLabel('', '10.0.0.7:51324')).toBe('51324');
  });
});
