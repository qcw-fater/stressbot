export type LuaCheckMode = 'action' | 'boolean' | 'listen' | 'free';

export interface SyntaxIssue {
  line: number;
  column: number;
  endLine: number;
  endColumn: number;
  severity: 'error' | 'warning' | 'info';
  message: string;
  source: 'syntax' | 'entry';
}

export interface ParseRequest {
  type: 'parse';
  requestId: number;
  code: string;
  mode: LuaCheckMode;
}

export interface ParseResponse {
  type: 'result';
  requestId: number;
  issues: SyntaxIssue[];
}

export interface LuaWorkerLike {
  onmessage: ((event: MessageEvent<ParseResponse>) => void) | null;
  onerror: ((event: ErrorEvent) => void) | null;
  postMessage(request: ParseRequest): void;
  terminate(): void;
}
