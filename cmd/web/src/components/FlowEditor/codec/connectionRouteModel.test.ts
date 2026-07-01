import { describe, expect, it } from 'vitest';
import {
  actionTargetConnection,
  serviceFromTargetConnection,
  targetConnectionValue,
} from './connectionRouteModel';

describe('connectionRouteModel', () => {
  it('action pattern 和 service 组合成目标连接', () => {
    expect(actionTargetConnection('tcpRequest', 'logic')).toEqual({ protocol: 'tcp', server: 'tcp:logic' });
    expect(actionTargetConnection('udpListen', 'battle')).toEqual({ protocol: 'udp', server: 'udp:battle' });
  });

  it('非 tcp/udp action 没有目标连接', () => {
    expect(actionTargetConnection('httpRequest', 'logic')).toBeNull();
    expect(actionTargetConnection('setState', 'logic')).toBeNull();
  });

  it('从目标连接反推 action 保存的 service', () => {
    expect(serviceFromTargetConnection('tcp:logic', 'tcp')).toBe('logic');
    expect(serviceFromTargetConnection('udp:battle', 'udp')).toBe('battle');
    expect(serviceFromTargetConnection('udp:battle', 'tcp')).toBeUndefined();
  });

  it('目标连接值优先使用已有 server，否则由 protocol/service 生成', () => {
    expect(targetConnectionValue({ server: 'tcp:logic' })).toBe('tcp:logic');
    expect(targetConnectionValue({ protocol: 'udp', service: 'battle' })).toBe('udp:battle');
    expect(targetConnectionValue({ protocol: 'tcp' })).toBeUndefined();
  });
});
