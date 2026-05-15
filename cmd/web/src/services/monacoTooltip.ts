/**
 * 全局 Monaco find-widget 中文 tooltip。
 *
 * 背景：全局 CSS 用 `display: none` 隐藏了 Monaco 的 `.workbench-hover-container`，
 * 以防止 find-widget 按钮的 tooltip 闪烁死循环。`.monaco-hover`（代码 hover 提示）不受影响。
 * 此模块在 document 级别监听 find-widget 按钮的 mouseover/mouseout，自绘中文 tooltip。
 *
 * 用法：在顶层组件调用 `useMonacoFindTooltip(themeMode)`。
 */

import { useEffect } from 'react';

const ARIA_TO_ZH: Record<string, string> = {
  'Previous Match': '上一个匹配 (Shift+Enter)',
  'Next Match': '下一个匹配 (Enter)',
  'Find in Selection': '在选区中查找 (Alt+L)',
  'Close (Escape)': '关闭 (Escape)',
  Close: '关闭 (Escape)',
  'Toggle Replace': '切换替换模式',
  'Toggle Replace mode': '切换替换模式',
  Replace: '替换',
  'Replace (Enter)': '替换 (Enter)',
  'Replace All': '全部替换',
  'Replace All (Ctrl+Alt+Enter)': '全部替换 (Ctrl+Alt+Enter)',
  'Match Case': '区分大小写 (Alt+C)',
  'Match Case (Alt+C)': '区分大小写 (Alt+C)',
  'Match Whole Word': '全字匹配 (Alt+W)',
  'Match Whole Word (Alt+W)': '全字匹配 (Alt+W)',
  'Use Regular Expression': '使用正则表达式 (Alt+R)',
  'Use Regular Expression (Alt+R)': '使用正则表达式 (Alt+R)',
  'Preserve Case': '保留大小写 (Alt+P)',
  'Preserve Case (Alt+P)': '保留大小写 (Alt+P)',
};

function lookupLabel(el: HTMLElement): string | null {
  const aria = el.getAttribute('aria-label') ?? '';
  if (ARIA_TO_ZH[aria]) return ARIA_TO_ZH[aria];
  const stripped = aria.replace(/\s*\([^)]*\)\s*$/, '').trim();
  return ARIA_TO_ZH[stripped] ?? null;
}

export function useMonacoFindTooltip(themeMode: string) {
  useEffect(() => {
    const dark = themeMode === 'dark';
    const tip = document.createElement('div');
    tip.style.cssText = [
      'position:fixed',
      'z-index:10000',
      'padding:4px 8px',
      'border-radius:4px',
      'font-size:12px',
      'line-height:1.4',
      'pointer-events:none',
      'opacity:0',
      'transition:opacity 0.12s ease',
      'white-space:nowrap',
      'box-shadow:0 2px 8px rgba(0,0,0,0.18)',
      `background:${dark ? '#3c3c3c' : '#ffffff'}`,
      `color:${dark ? '#e6e6e6' : '#333'}`,
      `border:1px solid ${dark ? '#5a5a5a' : '#d9d9d9'}`,
    ].join(';');
    document.body.appendChild(tip);

    let showTimer: number | null = null;
    let hideTimer: number | null = null;

    const onOver = (e: MouseEvent) => {
      const t = (e.target as HTMLElement | null)?.closest?.('.find-widget [aria-label]') as HTMLElement | null;
      if (!t) return;
      const label = lookupLabel(t);
      if (!label) return;
      if (hideTimer) { window.clearTimeout(hideTimer); hideTimer = null; }
      if (showTimer) window.clearTimeout(showTimer);
      showTimer = window.setTimeout(() => {
        tip.textContent = label;
        tip.style.opacity = '1';
        const r = t.getBoundingClientRect();
        const tr = tip.getBoundingClientRect();
        let left = r.left + r.width / 2 - tr.width / 2;
        let top = r.top - tr.height - 6;
        left = Math.max(4, Math.min(left, window.innerWidth - tr.width - 4));
        if (top < 4) top = r.bottom + 6;
        tip.style.left = `${left}px`;
        tip.style.top = `${top}px`;
      }, 350);
    };

    const onOut = (e: MouseEvent) => {
      const t = (e.target as HTMLElement | null)?.closest?.('.find-widget [aria-label]');
      if (!t) return;
      if (showTimer) { window.clearTimeout(showTimer); showTimer = null; }
      hideTimer = window.setTimeout(() => { tip.style.opacity = '0'; }, 80);
    };

    document.addEventListener('mouseover', onOver);
    document.addEventListener('mouseout', onOut);

    return () => {
      document.removeEventListener('mouseover', onOver);
      document.removeEventListener('mouseout', onOut);
      if (showTimer) window.clearTimeout(showTimer);
      if (hideTimer) window.clearTimeout(hideTimer);
      tip.remove();
    };
  }, [themeMode]);
}
