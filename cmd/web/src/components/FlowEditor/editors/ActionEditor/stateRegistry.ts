/**
 * State key 注册表：扫描 flow graph + 启动配置 + Lua 脚本中所有已知 state key，
 * 供绑定编辑器自动补全。
 *
 * 数据来源：
 *   1. ActionDef.store[].setter          — S2C 响应写入（有 s2cProto）
 *   2. ListenDef.store[].setter          — 推送监听写入（有 s2cProto）
 *   3. RobotConfig.stateExtra            — 启动前写入的扩展字段
 *   4. 当前 action 的 bindings[].storeAs — 中间变量（仅限当前 action）
 *   5. Lua 脚本中的 robot.set("key",…)   — 静态正则提取，无类型信息
 */

import type { ActionDef, FieldBind } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import type { FlowNode } from '@/types/flow';
import type { ProtoField } from '@/types/proto';
import { protoRegistry } from '../../proto/ProtoRegistry';

export interface StateKeyInfo {
  key: string;
  sourceType: 'store' | 'listenStore' | 'stateExtra' | 'storeAs' | 'lua';
  sourceName: string;
  s2cProto?: string;
  storeField?: string;
}

/** 从 Lua 脚本内容中提取 robot.set("key",…) 的字面量 key */
function extractLuaSetKeys(content: string): string[] {
  const keys: string[] = [];
  const re = /robot\.set\s*\(\s*["']([^"']+)["']/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(content)) !== null) {
    keys.push(m[1]);
  }
  return keys;
}

/**
 * 收集所有已知 state key。
 * storeAs 仅收集 currentBindings（当前 action 的 bindings）。
 * luaScripts 为 { name, content }[] 批量传入（由调用方从 IDB 异步加载）。
 */
export function collectStateKeys(
  actions: Record<string, ActionDef>,
  listens: Record<string, ListenDef>,
  stateExtra: Record<string, string> | undefined,
  currentBindings: FieldBind[] | undefined,
  luaScripts: Array<{ name: string; content: string }> | undefined,
): StateKeyInfo[] {
  const map = new Map<string, StateKeyInfo>();

  // 1. ActionDef.store[].setter
  for (const [name, def] of Object.entries(actions)) {
    if (!def.store) continue;
    for (const sm of def.store) {
      if (!sm.setter) continue;
      map.set(sm.setter, {
        key: sm.setter,
        sourceType: 'store',
        sourceName: name,
        s2cProto: def.s2cProto,
        storeField: sm.field,
      });
    }
  }

  // 2. ListenDef.store[].setter
  for (const [name, def] of Object.entries(listens)) {
    if (!def.store) continue;
    for (const sm of def.store) {
      if (!sm.setter) continue;
      if (map.has(sm.setter)) continue;
      map.set(sm.setter, {
        key: sm.setter,
        sourceType: 'listenStore',
        sourceName: name,
        s2cProto: def.s2cProto,
        storeField: sm.field,
      });
    }
  }

  // 3. stateExtra（启动配置）
  if (stateExtra) {
    for (const k of Object.keys(stateExtra)) {
      if (!k || map.has(k)) continue;
      map.set(k, {
        key: k,
        sourceType: 'stateExtra',
        sourceName: '启动配置',
      });
    }
  }

  // 4. 当前 action 的 bindings[].storeAs
  if (currentBindings) {
    for (const b of currentBindings) {
      if (!b.storeAs) continue;
      if (map.has(b.storeAs)) continue;
      map.set(b.storeAs, {
        key: b.storeAs,
        sourceType: 'storeAs',
        sourceName: '当前节点',
      });
    }
  }

  // 5. Lua 脚本中的 robot.set("key",…)
  if (luaScripts) {
    for (const script of luaScripts) {
      const extracted = extractLuaSetKeys(script.content);
      for (const k of extracted) {
        if (map.has(k)) continue;
        map.set(k, {
          key: k,
          sourceType: 'lua',
          sourceName: script.name,
        });
      }
    }
  }

  return Array.from(map.values()).sort((a, b) => a.key.localeCompare(b.key));
}

/** 从 flow graph 中提取实际引用的 Lua 脚本名称集合 */
export function collectUsedScriptNames(
  actions: Record<string, { pattern?: string; script?: string }>,
  listens: Record<string, { script?: string }>,
  nodes: Record<string, FlowNode> | undefined,
): Set<string> {
  const names = new Set<string>();
  for (const def of Object.values(actions)) {
    if (def.pattern === 'lua' && def.script) names.add(def.script);
  }
  for (const def of Object.values(listens)) {
    if (def.script) names.add(def.script);
  }
  // boolean / loop 节点的 condition / breakCondition 可以引用 lua: 脚本
  if (nodes) {
    for (const node of Object.values(nodes)) {
      for (const cond of [node.condition, node.breakCondition]) {
        if (cond?.startsWith('lua:')) {
          const name = cond.slice(4).trim();
          if (name) names.add(name);
        }
      }
    }
  }
  return names;
}

/**
 * 根据字段路径在 proto 消息中逐步解析，找到最终的消息类型全名。
 * 例如 "gameModeMap" 在 "Game.Response" 中是 map<uint32, GameModeInfo>，
 * 则返回 "GameModeInfo"（map value 的消息类型）。
 * 如果路径中遇到标量类型或找不到字段，返回 undefined。
 */
function resolveFieldProto(
  rootProto: string,
  fieldPath: string,
): string | undefined {
  if (!protoRegistry.isLoaded()) return undefined;

  // 去掉数组下标：items[0].id → items.id
  const cleanPath = fieldPath.replace(/\[\d+\]/g, '');
  const parts = cleanPath.split('.').filter(Boolean);
  let currentMsg = rootProto;

  for (const part of parts) {
    const field = protoRegistry.resolveField(currentMsg, part);
    if (!field) return undefined;

    if (field.kind === 'message' && field.messageName) {
      currentMsg = field.messageName;
    } else if (field.kind === 'map') {
      // map 的 value 如果是消息类型，继续向下解析
      if (field.messageName) {
        currentMsg = field.messageName;
      } else {
        return undefined; // map value 是标量，无法继续
      }
    } else {
      return undefined; // 标量/枚举，无法继续
    }
  }

  return currentMsg;
}

/** 格式化 proto 字段为可读类型字符串 */
function formatFieldType(field: ProtoField): string {
  if (field.kind === 'map') {
    const valName = field.messageName
      ? field.messageName.split('.').pop()!
      : field.mapValue ?? field.type;
    return `map<${field.mapKey}, ${valName}>`;
  }
  const baseName = field.kind === 'message'
    ? field.messageName?.split('.').pop() ?? field.type
    : field.kind === 'enum'
      ? field.enumName?.split('.').pop() ?? field.type
      : field.type;
  return field.repeated ? `${baseName}[]` : baseName;
}

/** 沿字段路径解析到最后一个字段的类型描述 */
function resolveFieldTypeInfo(rootProto: string, fieldPath: string): string | undefined {
  if (!protoRegistry.isLoaded()) return undefined;

  const cleanPath = fieldPath.replace(/\[\d+\]/g, '');
  const parts = cleanPath.split('.').filter(Boolean);
  if (parts.length === 0) return undefined;

  let currentMsg = rootProto;

  for (let i = 0; i < parts.length - 1; i++) {
    const f = protoRegistry.resolveField(currentMsg, parts[i]);
    if (!f) return undefined;
    if (f.kind === 'message' && f.messageName) {
      currentMsg = f.messageName;
    } else if (f.kind === 'map' && f.messageName) {
      currentMsg = f.messageName;
    } else {
      return undefined;
    }
  }

  const last = protoRegistry.resolveField(currentMsg, parts[parts.length - 1]);
  if (!last) return undefined;
  return formatFieldType(last);
}

/**
 * 解析 state key 的可读类型字符串，用于下拉列表展示。
 *
 * - storeField 存在：解析字段路径，显示最终字段类型（如 HeroInfo[]、uint64）
 * - storeField 为空（存整个消息）：显示消息短名
 * - 无 s2cProto：undefined
 */
export function resolveStateKeyDisplayType(info: StateKeyInfo): string | undefined {
  if (!info.s2cProto) return undefined;

  if (info.storeField) {
    return resolveFieldTypeInfo(info.s2cProto, info.storeField);
  }

  return info.s2cProto.split('.').pop() ?? info.s2cProto;
}

/**
 * 解析 state key 对应的 path 级联选择器应使用的 proto 消息全名。
 *
 * - storeField 为空（存整个消息）→ 返回 s2cProto
 * - storeField 非空 → 沿字段路径解析到最终的消息类型
 * - 无 s2cProto → undefined（普通文本输入）
 */
export function resolveProtoForStateKey(
  keys: StateKeyInfo[],
  stateKey: string,
): string | undefined {
  const info = keys.find((k) => k.key === stateKey);
  if (!info?.s2cProto) return undefined;

  if (!info.storeField) return info.s2cProto;

  return resolveFieldProto(info.s2cProto, info.storeField) ?? info.s2cProto;
}
