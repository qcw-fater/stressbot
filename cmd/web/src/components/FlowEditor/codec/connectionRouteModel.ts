import type { ActionPattern } from '@/types/action';
import type { CodecProtocol } from '@/services/codecConnections';
import { buildCodecConnectionName } from '@/services/codecConnections';

export interface ActionTargetConnection {
  protocol: CodecProtocol;
  server?: string;
}

export function actionTargetConnection(pattern: ActionPattern, service?: string): ActionTargetConnection | null {
  const protocol = actionProtocol(pattern);
  if (!protocol) return null;
  return {
    protocol,
    server: service ? buildCodecConnectionName(protocol, service) : undefined,
  };
}

export function actionProtocol(pattern: ActionPattern): CodecProtocol | null {
  if (pattern.startsWith('tcp')) return 'tcp';
  if (pattern.startsWith('udp')) return 'udp';
  return null;
}

export function serviceFromTargetConnection(server: string | undefined, protocol: CodecProtocol): string | undefined {
  if (!server) return undefined;
  const prefix = `${protocol}:`;
  if (!server.startsWith(prefix)) return undefined;
  const service = server.slice(prefix.length);
  return service || undefined;
}

export function targetConnectionValue(input: { server?: string; protocol?: CodecProtocol; service?: string }): string | undefined {
  if (input.server) return input.server;
  if (!input.protocol || !input.service) return undefined;
  return buildCodecConnectionName(input.protocol, input.service);
}
