import { describe, expect, it } from 'vitest';
import {
  buildCodecConnectionName,
  codecFileNameToConnNameStrict,
  connNameToCodecFileName,
  parseCodecFileNameStrict,
  validateCodecCreateInput,
} from '../codecConnections';

describe('codec connection naming', () => {
  it('由 protocol 和 service 生成连接名', () => {
    expect(buildCodecConnectionName('tcp', 'logic')).toBe('tcp:logic');
    expect(buildCodecConnectionName('udp', 'battle')).toBe('udp:battle');
  });

  it('连接名和 codec 文件名互转', () => {
    expect(connNameToCodecFileName('udp:battle')).toBe('udp_battle_codec.json');
    expect(codecFileNameToConnNameStrict('tcp_logic_codec.json')).toBe('tcp:logic');
    expect(parseCodecFileNameStrict('udp_battle_codec.json')).toMatchObject({
      protocol: 'udp',
      service: 'battle',
      conn: 'udp:battle',
      fileName: 'udp_battle_codec.json',
    });
  });

  it('严格拒绝非法 codec 文件名', () => {
    expect(() => codecFileNameToConnNameStrict('tcp-logic_codec.json')).toThrow(/非法/);
    expect(() => codecFileNameToConnNameStrict('http_logic_codec.json')).toThrow(/protocol|tcp|udp/);
    expect(() => codecFileNameToConnNameStrict('tcp__codec.json')).toThrow(/service/);
    expect(() => codecFileNameToConnNameStrict('tcp_logic.json')).toThrow(/_codec\.json/);
  });
});

describe('validateCodecCreateInput', () => {
  it('拒绝无效 protocol 或 service', () => {
    expect(validateCodecCreateInput('http', 'logic', [])).toMatch(/protocol|tcp|udp/);
    expect(validateCodecCreateInput('tcp', '', [])).toMatch(/service/);
    expect(validateCodecCreateInput('tcp', 'bad:name', [])).toMatch(/service/);
    expect(validateCodecCreateInput('tcp', 'bad_name', [])).toMatch(/service/);
  });

  it('拒绝重复连接并接受合法输入', () => {
    expect(validateCodecCreateInput('tcp', 'logic', ['tcp_logic_codec.json'])).toMatch(/已存在/);
    expect(validateCodecCreateInput('tcp', 'logic', [])).toBeNull();
  });
});
