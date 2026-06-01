/**
 * 不透明路由编辑器：单 Input 接受 JSON 字符串。
 *
 * 与 DeclarativeForm 中"不透明路由 route"字段保持一致：
 *   - 合法 JSON：解析后提交给 route
 *   - 非法 JSON：仅保留为输入框本地 draft，不写入 route
 *   - 空字符串：返回 undefined
 */

import { JsonDraftInput } from '../editors/shared/JsonDraftInput';
import { monoCellStyle } from '../styles/inlineStyles';

export interface RouteEditorProps {
  value: unknown;
  onChange: (v: unknown) => void;
  placeholder?: string;
  /** 输入框尺寸（用于嵌入表格的紧凑场景） */
  size?: 'small' | 'middle' | 'large';
}

export function RouteEditor({ value, onChange, placeholder, size }: RouteEditorProps) {
  return (
    <JsonDraftInput
      mode="json"
      value={value}
      emptyValue={undefined}
      onChange={onChange}
      placeholder={placeholder ?? '如 {"cmd":4,"act":10}'}
      size={size}
      style={monoCellStyle}
    />
  );
}
