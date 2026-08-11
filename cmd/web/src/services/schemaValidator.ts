import Ajv2020, { type ErrorObject, type ValidateFunction } from 'ajv/dist/2020';
import flowSchema from '../../../../schemas/flow.schema.json';
import codecSchema from '../../../../schemas/codec.schema.json';

export interface SchemaIssue {
  /** JSON Pointer；根对象使用 /。 */
  path: string;
  /** 不包含配置值的中文结构错误。 */
  message: string;
  keyword: string;
}

const ajv = new Ajv2020({ allErrors: true, strict: true });
ajv.addSchema(flowSchema);
const validateFlow = ajv.getSchema(flowSchema.$id) as ValidateFunction;
const validateCodec = ajv.compile(codecSchema);

export function validateFlowStructure(value: unknown): SchemaIssue[] {
  return validateWith(validateFlow, value);
}

export function validateCodecStructure(value: unknown): SchemaIssue[] {
  return validateWith(validateCodec, value);
}

function validateWith(validate: ValidateFunction, value: unknown): SchemaIssue[] {
  if (validate(value)) return [];
  return (validate.errors ?? []).map(toIssue);
}

function toIssue(error: ErrorObject): SchemaIssue {
  const path = pointerFor(error);
  return {
    path,
    keyword: error.keyword,
    message: messageFor(error),
  };
}

function pointerFor(error: ErrorObject): string {
  let path = error.instancePath || '';
  if (error.keyword === 'required') {
    path += `/${escapePointer(String(error.params.missingProperty ?? ''))}`;
  } else if (
    error.keyword === 'unevaluatedProperties' ||
    error.keyword === 'additionalProperties'
  ) {
    const property = error.params.unevaluatedProperty ?? error.params.additionalProperty;
    path += `/${escapePointer(String(property ?? ''))}`;
  }
  return path || '/';
}

function messageFor(error: ErrorObject): string {
  switch (error.keyword) {
    case 'required':
      return '缺少必填字段';
    case 'type':
      return `类型不正确，应为 ${String(error.params.type ?? '指定类型')}`;
    case 'enum':
    case 'const':
      return '值不在允许范围内';
    case 'minimum':
      return `数值不能小于 ${String(error.params.limit ?? '')}`;
    case 'minLength':
      return '字符串不能为空';
    case 'minItems':
      return '数组元素数量不足';
    case 'unevaluatedProperties':
    case 'additionalProperties':
      return '包含未定义字段';
    default:
      return `结构不符合约束（${error.keyword}）`;
  }
}

function escapePointer(value: string): string {
  return value.replace(/~/g, '~0').replace(/\//g, '~1');
}
