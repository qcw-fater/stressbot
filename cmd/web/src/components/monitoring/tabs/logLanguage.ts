/**
 * Monaco custom language: stressbot-log
 *
 * Monarch state-machine tokenizer — parses fields by position,
 * no wrapping markers needed:
 *   TIMESTAMP  LEVEL(pad7)  CALLER  SERVICE  MESSAGE  JSON
 */

import type { Monaco } from '@monaco-editor/react';

const LANGUAGE_ID = 'stressbot-log';

let registered = false;

export function registerLogLanguage(monaco: Monaco) {
  if (registered) return;
  registered = true;

  monaco.languages.register({ id: LANGUAGE_ID });

  monaco.languages.setMonarchTokensProvider(LANGUAGE_ID, {
    tokenizer: {
      tsGuard: [
        [/\d{4}\/\d{2}\/\d{2}\s\d{2}:\d{2}:\d{2}\.\d+[+-]\d{4}/, { token: 'log-timestamp', next: '@sep1' }],
      ],
      root: [
        { include: '@tsGuard' },
      ],
      sep1: [
        { include: '@tsGuard' },
        [/ {2}/, { token: '', next: '@expectLevel' }],
      ],
      expectLevel: [
        { include: '@tsGuard' },
        [/dpanic /, { token: 'log-level-fatal', next: '@fieldCaller' }],
        [/panic {2}/, { token: 'log-level-fatal', next: '@fieldCaller' }],
        [/fatal {2}/, { token: 'log-level-fatal', next: '@fieldCaller' }],
        [/error {2}/, { token: 'log-level-error', next: '@fieldCaller' }],
        [/warn {3}/, { token: 'log-level-warn', next: '@fieldCaller' }],
        [/info {3}/, { token: 'log-level-info', next: '@fieldCaller' }],
        [/debug {2}/, { token: 'log-level-debug', next: '@fieldCaller' }],
      ],
      fieldCaller: [
        { include: '@tsGuard' },
        [/\S+/, { token: 'log-source', next: '@sep3' }],
        [/ {2}/, { token: '', next: '@fieldService' }],
      ],
      sep3: [
        { include: '@tsGuard' },
        [/ {2}/, { token: '', next: '@fieldService' }],
      ],
      fieldService: [
        { include: '@tsGuard' },
        [/\S+/, { token: 'log-source', next: '@sep4' }],
        [/ {2}/, { token: '', next: '@message' }],
      ],
      sep4: [
        { include: '@tsGuard' },
        [/ {2}/, { token: '', next: '@message' }],
      ],
      message: [
        { include: '@tsGuard' },
        [/\{[^}]*\}$/, { token: 'log-json' }],
        [/./, ''],
      ],
    },
  });

  monaco.editor.defineTheme('stressbot-log-dark', {
    base: 'vs-dark',
    inherit: true,
    rules: [
      { token: 'log-timestamp', foreground: '565f89' },
      { token: 'log-level-debug', foreground: '7982a9' },
      { token: 'log-level-info', foreground: '7aa2f7' },
      { token: 'log-level-warn', foreground: 'e0af68' },
      { token: 'log-level-error', foreground: 'f7768e' },
      { token: 'log-level-fatal', foreground: 'bb9af7' },
      { token: 'log-source', foreground: '7dcfff' },
      { token: 'log-json', foreground: '9ece6a' },
    ],
    colors: {
      'editor.background': '#1a1b26',
      'editor.foreground': '#c0caf5',
      'editor.lineHighlightBackground': '#1e202e',
      'editor.selectionBackground': '#33467c',
    },
  });

  monaco.editor.defineTheme('stressbot-log-light', {
    base: 'vs',
    inherit: true,
    rules: [
      { token: 'log-timestamp', foreground: 'b0bac6' },
      { token: 'log-level-debug', foreground: '7888a0' },
      { token: 'log-level-info', foreground: '2b78ef' },
      { token: 'log-level-warn', foreground: 'efa030' },
      { token: 'log-level-error', foreground: 'f06070' },
      { token: 'log-level-fatal', foreground: 'a88aff' },
      { token: 'log-source', foreground: '22bcd8' },
      { token: 'log-json', foreground: '55c070' },
    ],
    colors: {
      'editor.background': '#f8f9fb',
      'editor.foreground': '#1f2937',
      'editor.lineHighlightBackground': '#f0f2f5',
      'editor.selectionBackground': '#bbd5f8',
    },
  });
}

export function getLogTheme(dark: boolean): string {
  return dark ? 'stressbot-log-dark' : 'stressbot-log-light';
}
