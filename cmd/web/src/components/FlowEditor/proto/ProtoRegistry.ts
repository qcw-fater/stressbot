/**
 * Proto 描述符索引：把 protobufjs Root 投影成轻量化的 ProtoMessage / ProtoEnum 索引。
 *
 * 提供：
 *   - lookupMessage(fullName)
 *   - listMessages(prefix?)
 *   - resolveField(messageName, fieldName)
 *   - listEnums()
 */

import * as protobuf from 'protobufjs';
import type { ProtoEnum, ProtoField, ProtoFieldKind, ProtoMessage } from '@/types/proto';

export class ProtoRegistry {
  private messages = new Map<string, ProtoMessage>();
  private enums = new Map<string, ProtoEnum>();
  private root?: protobuf.Root;

  load(root: protobuf.Root) {
    this.root = root;
    this.messages.clear();
    this.enums.clear();
    walk(root, (ns) => {
      if (ns instanceof protobuf.Type) {
        const msg = this.toMessage(ns);
        this.messages.set(msg.fullName, msg);
      } else if (ns instanceof protobuf.Enum) {
        this.enums.set(ns.fullName.replace(/^\./, ''), this.toEnum(ns));
      }
    });
  }

  lookupMessage(fullName: string): ProtoMessage | undefined {
    return this.messages.get(fullName) ?? this.messages.get(fullName.replace(/^\./, ''));
  }

  lookupEnum(fullName: string): ProtoEnum | undefined {
    return this.enums.get(fullName) ?? this.enums.get(fullName.replace(/^\./, ''));
  }

  listMessages(prefix?: string): ProtoMessage[] {
    const all = Array.from(this.messages.values());
    if (!prefix) return all.sort((a, b) => a.fullName.localeCompare(b.fullName));
    return all
      .filter((m) => m.fullName.startsWith(prefix) || m.shortName.includes(prefix))
      .sort((a, b) => a.fullName.localeCompare(b.fullName));
  }

  listEnums(): ProtoEnum[] {
    return Array.from(this.enums.values()).sort((a, b) => a.fullName.localeCompare(b.fullName));
  }

  resolveField(messageFullName: string, fieldName: string): ProtoField | undefined {
    const msg = this.lookupMessage(messageFullName);
    return msg?.fields.find((f) => f.name === fieldName);
  }

  isLoaded(): boolean {
    return !!this.root;
  }

  private toMessage(t: protobuf.Type): ProtoMessage {
    return {
      fullName: t.fullName.replace(/^\./, ''),
      shortName: t.name,
      file: (t as unknown as { filename?: string }).filename,
      fields: Object.values(t.fields).map((f) => this.toField(f)),
      comment: t.comment || undefined,
    };
  }

  private toField(f: protobuf.Field): ProtoField {
    let kind: ProtoFieldKind = 'scalar';
    if (f instanceof protobuf.MapField) kind = 'map';
    else if (f.resolvedType instanceof protobuf.Enum) kind = 'enum';
    else if (f.resolvedType instanceof protobuf.Type) kind = 'message';

    return {
      name: f.name,
      number: f.id,
      type: f.type,
      kind,
      repeated: f.repeated,
      optional: f.optional,
      mapKey: f instanceof protobuf.MapField ? f.keyType : undefined,
      mapValue: f instanceof protobuf.MapField ? f.type : undefined,
      enumName: f.resolvedType instanceof protobuf.Enum ? f.resolvedType.fullName.replace(/^\./, '') : undefined,
      messageName: f.resolvedType instanceof protobuf.Type ? f.resolvedType.fullName.replace(/^\./, '') : undefined,
      comment: f.comment || undefined,
    };
  }

  private toEnum(e: protobuf.Enum): ProtoEnum {
    return {
      fullName: e.fullName.replace(/^\./, ''),
      values: { ...e.values },
    };
  }
}

/** 单例（启动时全量加载，整个会话复用） */
export const protoRegistry = new ProtoRegistry();

/** 递归遍历 protobufjs 命名空间树。
 *  注意：protobuf.NamespaceBase 仅在 .d.ts 中以类型存在，运行时不导出 —— 用 nestedArray 鸭子类型判断。 */
function walk(ns: protobuf.Namespace, cb: (n: protobuf.ReflectionObject) => void): void {
  cb(ns);
  if (!ns.nestedArray) return;
  for (const child of ns.nestedArray) {
    if ((child as { nestedArray?: unknown }).nestedArray !== undefined) {
      walk(child as protobuf.Namespace, cb);
    } else {
      cb(child);
    }
  }
}
