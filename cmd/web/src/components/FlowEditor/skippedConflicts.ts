const SKIPPED_KEY = 'stressbot:skippedConflicts';

export function hashContent(s: string): string {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = ((h << 5) - h + s.charCodeAt(i)) | 0;
  return String(h);
}

export function loadSkippedConflicts(): Set<string> {
  try {
    const raw = localStorage.getItem(SKIPPED_KEY);
    if (!raw) return new Set();
    return new Set(JSON.parse(raw) as string[]);
  } catch { return new Set(); }
}

export function saveSkippedConflict(key: string): void {
  const set = loadSkippedConflicts();
  set.add(key);
  try { localStorage.setItem(SKIPPED_KEY, JSON.stringify([...set])); } catch { /* */ }
}
