import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import type { TaskFlow } from '@/types/flow';
import { validateFlow } from './refsCheck';

const flowDir = path.resolve(process.cwd(), '../../conf/flow');

describe('仓库流程配置', () => {
  for (const file of ['flow.json', 'normal.json', 'rank_alone.json', 'spectator.json']) {
    it(`${file} 没有校验错误`, () => {
      const flow = JSON.parse(fs.readFileSync(path.join(flowDir, file), 'utf8')) as TaskFlow;
      const report = validateFlow(flow);

      expect(report.errors, report.errors.map((issue) => `${issue.code}: ${issue.message}`).join('\n')).toEqual([]);
    });
  }
});
