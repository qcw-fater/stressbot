import { describe, expect, it } from 'vitest';
import { validateCodecStructure, validateFlowStructure } from './schemaValidator';

import validFlow from '../../../../schemas/testdata/valid/flow-all-kinds.json';
import validCodec from '../../../../schemas/testdata/valid/codec-pipeline.json';
import invalidFlowExtra from '../../../../schemas/testdata/invalid/flow-extra-property.json';
import invalidFlowMissing from '../../../../schemas/testdata/invalid/flow-missing-required.json';
import invalidFlowQueue from '../../../../schemas/testdata/invalid/flow-negative-queue.json';
import invalidFlowEnum from '../../../../schemas/testdata/invalid/flow-unknown-enum.json';
import invalidFlowType from '../../../../schemas/testdata/invalid/flow-wrong-type.json';
import invalidCodecExtra from '../../../../schemas/testdata/invalid/codec-extra-property.json';
import invalidCodecMissing from '../../../../schemas/testdata/invalid/codec-missing-required.json';
import invalidCodecRange from '../../../../schemas/testdata/invalid/codec-out-of-range.json';
import invalidCodecEnum from '../../../../schemas/testdata/invalid/codec-unknown-enum.json';
import invalidCodecType from '../../../../schemas/testdata/invalid/codec-wrong-type.json';

describe('shared JSON Schema corpus', () => {
  it.each([
    ['flow-all-kinds.json', validateFlowStructure, validFlow],
    ['codec-pipeline.json', validateCodecStructure, validCodec],
  ])('accepts valid/%s', (_name, validate, value) => {
    expect(validate(value)).toEqual([]);
  });

  it.each([
    ['flow-extra-property.json', validateFlowStructure, invalidFlowExtra],
    ['flow-missing-required.json', validateFlowStructure, invalidFlowMissing],
    ['flow-negative-queue.json', validateFlowStructure, invalidFlowQueue],
    ['flow-unknown-enum.json', validateFlowStructure, invalidFlowEnum],
    ['flow-wrong-type.json', validateFlowStructure, invalidFlowType],
    ['codec-extra-property.json', validateCodecStructure, invalidCodecExtra],
    ['codec-missing-required.json', validateCodecStructure, invalidCodecMissing],
    ['codec-out-of-range.json', validateCodecStructure, invalidCodecRange],
    ['codec-unknown-enum.json', validateCodecStructure, invalidCodecEnum],
    ['codec-wrong-type.json', validateCodecStructure, invalidCodecType],
  ])('rejects invalid/%s with a JSON pointer', (_name, validate, value) => {
    const issues = validate(value);
    expect(issues.length).toBeGreaterThan(0);
    expect(issues.every((issue) => issue.path.startsWith('/'))).toBe(true);
    expect(issues.every((issue) => issue.message.length > 0)).toBe(true);
  });

  it('does not include configuration values in error messages', () => {
    const secret = 'do-not-log-this-value';
    const issues = validateFlowStructure({ ...invalidFlowType, secret });
    expect(JSON.stringify(issues)).not.toContain(secret);
  });
});
