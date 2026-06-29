import { listCodecFiles, type ResourceFile } from './resourcesStore';

export const CODEC_PROTOCOLS = ['tcp', 'udp'] as const;
export type CodecProtocol = (typeof CODEC_PROTOCOLS)[number];

export const CODEC_FILE_SUFFIX = '_codec.json';

export interface CodecConnection {
  protocol: CodecProtocol;
  service: string;
  conn: string;
  fileName: string;
}

export interface CodecRouteSpec extends CodecConnection {
  routeKeyTemplate: string;
}

export function isCodecProtocol(value: string): value is CodecProtocol {
  return (CODEC_PROTOCOLS as readonly string[]).includes(value);
}

export function buildCodecConnectionName(protocol: CodecProtocol, service: string): string {
  return `${protocol}:${service.trim()}`;
}

export function connNameToCodecFileName(conn: string): string {
  return `${conn.replace(':', '_')}${CODEC_FILE_SUFFIX}`;
}

export function codecFileNameToConnNameStrict(name: string): string {
  return parseCodecFileNameStrict(name).conn;
}

export function parseCodecFileNameStrict(name: string): CodecConnection {
  if (!name.endsWith(CODEC_FILE_SUFFIX)) {
    throw new Error(`非法 codec 文件名：${name}，必须以 ${CODEC_FILE_SUFFIX} 结尾`);
  }
  const stripped = name.slice(0, -CODEC_FILE_SUFFIX.length);
  const idx = stripped.indexOf('_');
  if (idx <= 0 || idx === stripped.length - 1) {
    throw new Error(`非法 codec 文件名：${name}，格式应为 <protocol>_<service>${CODEC_FILE_SUFFIX}`);
  }
  const protocol = stripped.slice(0, idx);
  const service = stripped.slice(idx + 1);
  if (!isCodecProtocol(protocol)) {
    throw new Error(`非法 codec protocol：${protocol}，只能是 tcp 或 udp`);
  }
  if (!service || service.includes(':') || service.includes('_')) {
    throw new Error(`非法 codec service：${service || '(空)'}，service 不能为空且不能包含 ":" 或 "_"`);
  }
  const conn = buildCodecConnectionName(protocol, service);
  return { protocol, service, conn, fileName: name };
}

export function validateCodecCreateInput(
  protocol: string,
  service: string,
  existingFileNames: string[],
): string | null {
  const p = protocol.trim();
  const s = service.trim();
  if (!isCodecProtocol(p)) {
    return `protocol 必须是 tcp 或 udp（当前 ${p || '空'}）`;
  }
  if (!s || s.includes(':') || s.includes('_')) {
    return 'service 不能为空，也不能包含 ":" 或 "_"';
  }
  const fileName = connNameToCodecFileName(buildCodecConnectionName(p, s));
  if (existingFileNames.includes(fileName)) {
    return `连接 ${buildCodecConnectionName(p, s)} 已存在`;
  }
  return null;
}

export function collectCodecConnectionsFromFiles(files: ResourceFile[]): CodecConnection[] {
  return files.map((f) => parseCodecFileNameStrict(f.name));
}

export async function listCodecConnections(): Promise<CodecConnection[]> {
  return collectCodecConnectionsFromFiles(await listCodecFiles());
}

export async function loadCodecRouteSpecs(options: { strict?: boolean } = {}): Promise<Map<string, CodecRouteSpec>> {
  const strict = options.strict ?? true;
  const files = await listCodecFiles();
  const map = new Map<string, CodecRouteSpec>();
  for (const f of files) {
    let conn: CodecConnection;
    try {
      conn = parseCodecFileNameStrict(f.name);
    } catch (e) {
      if (strict) throw e;
      continue;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(f.content);
    } catch (e) {
      if (strict) throw new Error(`无法解析协议配置 ${f.name}：${(e as Error).message}`);
      continue;
    }
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      if (strict) throw new Error(`协议配置 ${f.name} 不是合法 JSON 对象`);
      continue;
    }
    const routeKeyTemplate = (parsed as { routeKeyTemplate?: unknown }).routeKeyTemplate;
    if (typeof routeKeyTemplate !== 'string') {
      if (strict) throw new Error(`协议配置 ${f.name} 缺少 routeKeyTemplate`);
      continue;
    }
    map.set(conn.conn, { ...conn, routeKeyTemplate });
  }
  return map;
}
