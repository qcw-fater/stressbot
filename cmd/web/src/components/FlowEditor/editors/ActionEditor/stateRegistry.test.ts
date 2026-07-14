import { describe, expect, it } from 'vitest';
import { collectStateKeys } from './stateRegistry';
import type { ActionDef } from '@/types/action';

function findKey(keys: ReturnType<typeof collectStateKeys>, key: string) {
  return keys.find((k) => k.key === key);
}

describe('collectStateKeys', () => {
  it('内置 index 描述为任务全局零基序号', () => {
    const keys = collectStateKeys({}, {}, undefined, undefined, undefined);

    expect(findKey(keys, 'index')).toMatchObject({
      sourceType: 'builtin',
      builtinType: 'int',
      builtinDesc: '任务全局序号（0-based，不含 startNumber 偏移）',
    });
  });

  it('嵌套 setter 不覆盖 Lua 已声明的顶层 state key 来源', () => {
    const actions: Record<string, ActionDef> = {
      guildLogin: {
        pattern: 'tcpRequest',
        s2cProto: 'Game.GuildLoginInfo',
        store: [{ field: 'GuildInfo', setter: 'playerData.GuildInfo' }],
      },
    };

    const keys = collectStateKeys(actions, {}, undefined, undefined, [
      { name: 'init.lua', content: 'robot.set("playerData", {})' },
    ]);

    expect(findKey(keys, 'playerData')).toMatchObject({
      key: 'playerData',
      sourceType: 'lua',
      sourceName: 'init.lua',
    });
    expect(findKey(keys, 'playerData')?.s2cProto).toBeUndefined();
  });

  it('嵌套 setter 仅在没有已有来源时注册顶层兜底 key，且不携带 S2C 类型', () => {
    const actions: Record<string, ActionDef> = {
      guildLogin: {
        pattern: 'tcpRequest',
        s2cProto: 'Game.GuildLoginInfo',
        store: [{ field: 'GuildInfo', setter: 'playerData.GuildInfo' }],
      },
    };

    const keys = collectStateKeys(actions, {}, undefined, undefined, undefined);

    expect(findKey(keys, 'playerData')).toMatchObject({
      key: 'playerData',
      sourceType: 'store',
      sourceName: 'guildLogin',
    });
    expect(findKey(keys, 'playerData')?.s2cProto).toBeUndefined();
  });

  it('扁平 setter 保持 S2C 类型信息，用于展开响应子字段', () => {
    const actions: Record<string, ActionDef> = {
      login: {
        pattern: 'tcpRequest',
        s2cProto: 'Game.LoginResp',
        store: [{ setter: 'loginResp' }],
      },
    };

    const keys = collectStateKeys(actions, {}, undefined, undefined, undefined);

    expect(findKey(keys, 'loginResp')).toMatchObject({
      key: 'loginResp',
      sourceType: 'store',
      sourceName: 'login',
      s2cProto: 'Game.LoginResp',
    });
  });

  it('Lua set_path 提取顶层 state key', () => {
    const keys = collectStateKeys({}, {}, undefined, undefined, [
      { name: 'init.lua', content: 'robot.set_path("playerData.GuildInfo", guild)' },
    ]);

    expect(findKey(keys, 'playerData')).toMatchObject({
      key: 'playerData',
      sourceType: 'lua',
      sourceName: 'init.lua',
    });
  });
});
