/**
 * Callback 三态视觉样式的单一事实源。
 *
 * 三处历史散落：CallbackPanel / CallbackEditor / ListenRefsTable 的下拉 Tag 等
 * 都引用同一份颜色，避免重复定义、漏改不同步。
 *
 * 注意：CallbackCard.css 中的 `.kind-tag-*` 是 CSS 类侧的同义实现（用 CSS token），
 * 与本文件保持语义一致：silent=灰、declarative=蓝、lua=紫。
 */

import type { CallbackKind } from '@/types/callback';

/** Ant Design `<Tag color>` 取值（preset color name） */
export const callbackKindTagColor: Record<CallbackKind, string> = {
  silent: 'default',
  declarative: 'blue',
  lua: 'purple',
};

/** 简短文案（用于浮卡 / 表格紧凑显示） */
export const callbackKindShortLabel: Record<CallbackKind, string> = {
  silent: 'silent',
  declarative: 'decl',
  lua: 'lua',
};
