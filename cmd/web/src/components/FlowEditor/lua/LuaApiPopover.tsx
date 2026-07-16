/**
 * Lua API 参考弹出面板 — 按模块分组展示所有内置函数。
 */

import { Badge, Collapse, Popover, Typography } from 'antd';
import { BookOutlined } from '@ant-design/icons';
import { useState } from 'react';
import { LUA_MODULES, renderSignature } from './luaApiSpec';
import type { LuaFunction, LuaModule } from './luaApiSpec';
import { useFloatingWindowStore } from '../store/floatingWindowStore';

const MODULE_COLOR: Record<string, string> = {
  robot: 'blue',
  network: 'green',
  proto: 'purple',
  utils: 'orange',
  json: 'cyan',
  log: 'default',
};

function FnCard({ fn }: { fn: LuaFunction }) {
  return (
    <div style={{ marginBottom: 8 }}>
      <Typography.Text code style={{ fontSize: 12 }}>
        {fn.module}.{fn.name}
        {renderSignature(fn)}
      </Typography.Text>
      <div style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 2 }}>
        {fn.summary}
      </div>
      {fn.params.length > 0 && (
        <div style={{ fontSize: 10, color: 'var(--text-tertiary)', marginTop: 2 }}>
          {fn.params.map((p) => (
            <span key={p.name} style={{ marginRight: 8 }}>
              <Typography.Text
                type="secondary"
                style={{ fontSize: 10 }}
              >{`${p.name}: ${p.type}${p.optional ? '?' : ''}`}</Typography.Text>
            </span>
          ))}
        </div>
      )}
      <div style={{ fontSize: 10, color: 'var(--text-tertiary)', marginTop: 1 }}>
        返回: <code style={{ fontSize: 10 }}>{fn.returns}</code>
      </div>
    </div>
  );
}

function ModuleSection({ mod }: { mod: LuaModule }) {
  return (
    <div>
      <div style={{ fontSize: 11, color: 'var(--text-tertiary)', marginBottom: 6 }}>
        {mod.summary}
      </div>
      {mod.functions.map((fn) => (
        <FnCard key={fn.name} fn={fn} />
      ))}
    </div>
  );
}

export function LuaApiPopover() {
  const [open, setOpen] = useState(false);
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;

  const content = (
    <div style={{ width: 420, maxHeight: '60vh', overflowY: 'auto' }}>
      <Collapse
        size="small"
        defaultActiveKey={LUA_MODULES.map((m) => m.name)}
        items={LUA_MODULES.map((mod) => ({
          key: mod.name,
          label: (
            <span>
              <Badge color={MODULE_COLOR[mod.name] ?? 'default'} style={{ marginRight: 6 }} />
              <Typography.Text strong style={{ fontSize: 12 }}>
                {mod.name}
              </Typography.Text>
              <Typography.Text type="secondary" style={{ fontSize: 10, marginLeft: 6 }}>
                ({mod.functions.length} 个函数)
              </Typography.Text>
            </span>
          ),
          children: <ModuleSection mod={mod} />,
        }))}
      />
    </div>
  );

  return (
    <Popover
      title="内置脚本接口参考"
      trigger="click"
      open={open}
      onOpenChange={setOpen}
      placement="bottomRight"
      content={content}
      overlayStyle={{ maxWidth: 500, zIndex: popupZ + 10 }}
    >
      <BookOutlined style={{ cursor: 'pointer', fontSize: 14, color: 'var(--text-secondary)' }} />
    </Popover>
  );
}
