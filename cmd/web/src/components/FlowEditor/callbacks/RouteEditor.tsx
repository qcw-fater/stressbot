/**
 * 不透明路由编辑器：单 Input 接受 JSON 字符串。
 *
 * 与 DeclarativeForm 中"不透明路由 route"字段保持一致：
 *   - 合法 JSON：解析后存为 object
 *   - 非法 JSON：原样作为 string 暂存（用户输入未完成）
 *   - 空字符串：返回 undefined
 */

import { Input } from 'antd';
import { monoCellStyle } from '../styles/inlineStyles';

export interface RouteEditorProps {
  value: unknown;
  onChange: (v: unknown) => void;
  placeholder?: string;
  /** 输入框尺寸（用于嵌入表格的紧凑场景） */
  size?: 'small' | 'middle' | 'large';
}

export function RouteEditor({ value, onChange, placeholder, size }: RouteEditorProps) {
  // 显示规则：string 直接用；其它（object / undefined）用 JSON.stringify
  const display = typeof value === 'string' ? value : JSON.stringify(value ?? {});

  return (
    <Input
      size={size}
      value={display}
      onChange={(e) => {
        const text = e.target.value;
        if (text === '') {
          onChange(undefined);
          return;
        }
        try {
          onChange(JSON.parse(text));
        } catch {
          // JSON 不合法时原样保留，等用户继续输入
          onChange(text);
        }
      }}
      placeholder={placeholder ?? '如 {"cmd":4,"act":10}'}
      style={monoCellStyle}
    />
  );
}
