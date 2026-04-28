/**
 * Proto 描述符的轻量化前端模型。
 * 由 protobufjs 解析后投影出来，便于 ProtoBrowser / 字段补全使用。
 */

export type ProtoFieldKind = 'scalar' | 'enum' | 'message' | 'map';

export interface ProtoField {
  name: string;
  number: number;
  type: string; // 原始类型字符串：int32 / string / Game.PlayerInfo / ...
  kind: ProtoFieldKind;
  repeated: boolean;
  optional?: boolean;
  mapKey?: string;
  mapValue?: string;
  enumName?: string;
  messageName?: string;
}

export interface ProtoMessage {
  fullName: string;
  shortName: string;
  file?: string;
  fields: ProtoField[];
  nestedMessages?: ProtoMessage[];
}

export interface ProtoEnum {
  fullName: string;
  values: Record<string, number>;
}

export interface ProtoFile {
  name: string;
  messages: ProtoMessage[];
  enums: ProtoEnum[];
}
