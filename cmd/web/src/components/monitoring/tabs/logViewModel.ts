export interface LogViewEntry {
  level: string;
  message: string;
  text: string;
}

export function filterLogEntries(
  entries: LogViewEntry[],
  level: string,
  filterText: string,
): LogViewEntry[] {
  const query = filterText.trim().toLowerCase();
  if (!level && !query) return entries;
  return entries.filter((entry) => {
    if (level && entry.level !== level) return false;
    return !query || entry.message.toLowerCase().includes(query);
  });
}

export type LogRenderPlan =
  | { kind: 'none'; entries: [] }
  | { kind: 'append'; entries: LogViewEntry[] }
  | { kind: 'replace'; entries: LogViewEntry[] };

export function planLogRender(
  previous: LogViewEntry[],
  next: LogViewEntry[],
  forceReplace: boolean,
): LogRenderPlan {
  if (forceReplace) return { kind: 'replace', entries: next };
  if (previous.length > next.length) return { kind: 'replace', entries: next };
  for (let index = 0; index < previous.length; index += 1) {
    if (previous[index] !== next[index]) return { kind: 'replace', entries: next };
  }
  if (previous.length === next.length) return { kind: 'none', entries: [] };
  return { kind: 'append', entries: next.slice(previous.length) };
}
