/**
 * 底部停靠的监控面板。
 *
 * 行为：
 *   - 编辑态：默认折叠成一条带，仅显示"展开监控"按钮
 *   - 运行/查看/finalReport：默认展开，6 个 Tab：大盘 / 动作 / 错误 / 趋势 / per-Agent / 系统
 *   - 高度可通过顶部拖把手调整（160px ~ 80vh）
 *   - 折叠态/展开态由 editorStore.monitorDockOpen 持久化
 */

import { Button, Tabs } from 'antd';
import { CaretDownOutlined, CaretUpOutlined, LineChartOutlined } from '@ant-design/icons';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useRuntimeStore } from '@/services';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { DashboardTab } from './tabs/DashboardTab';
import { ActionsTab } from './tabs/ActionsTab';
import { ErrorsTab } from './tabs/ErrorsTab';
import { TrendsTab } from './tabs/TrendsTab';
import { PerAgentTab } from './tabs/PerAgentTab';

const MIN_H = 160;
const MAX_H_RATIO = 0.8;
const DEFAULT_H = 360;

export function MonitorDock() {
  const mode = useRuntimeStore((s) => s.mode);
  const dockOpen = useEditorStore((s) => s.monitorDockOpen);
  const setDockOpen = useEditorStore((s) => s.setMonitorDockOpen);
  const [height, setHeight] = useState<number>(() => {
    const saved = Number(localStorage.getItem('stressbot.monitorDock.h'));
    return saved >= MIN_H ? saved : DEFAULT_H;
  });
  const dragRef = useRef<{ startY: number; startH: number } | null>(null);
  const [activeKey, setActiveKey] = useState('dashboard');

  // 自动按模式切换默认开关：编辑→关；运行→开；不强制覆盖用户已设置
  const lastModeRef = useRef(mode);
  useEffect(() => {
    if (lastModeRef.current === mode) return;
    if (lastModeRef.current === 'edit' && mode !== 'edit') setDockOpen(true);
    if (mode === 'edit') setDockOpen(false);
    lastModeRef.current = mode;
  }, [mode, setDockOpen]);

  const onDragStart = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault();
      dragRef.current = { startY: e.clientY, startH: height };
      const onMove = (ev: MouseEvent) => {
        if (!dragRef.current) return;
        const max = window.innerHeight * MAX_H_RATIO;
        const next = Math.max(MIN_H, Math.min(max, dragRef.current.startH + (dragRef.current.startY - ev.clientY)));
        setHeight(next);
      };
      const onUp = () => {
        if (dragRef.current) localStorage.setItem('stressbot.monitorDock.h', String(height));
        dragRef.current = null;
        window.removeEventListener('mousemove', onMove);
        window.removeEventListener('mouseup', onUp);
      };
      window.addEventListener('mousemove', onMove);
      window.addEventListener('mouseup', onUp);
    },
    [height],
  );

  if (mode === 'edit') return null;

  if (!dockOpen) {
    return (
      <div
        style={{
          height: 32,
          borderTop: '1px solid var(--border-color)',
          background: 'var(--bg-panel)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '0 12px',
          fontSize: 12,
        }}
      >
        <Button type="text" size="small" icon={<LineChartOutlined />} onClick={() => setDockOpen(true)}>
          展开监控
        </Button>
        <span style={{ color: 'var(--text-tertiary)' }}>
          点击展开实时监控
        </span>
        <Button type="text" size="small" icon={<CaretUpOutlined />} onClick={() => setDockOpen(true)} />
      </div>
    );
  }

  return (
    <div
      style={{
        height,
        borderTop: '1px solid var(--border-color)',
        background: 'var(--bg-panel)',
        display: 'flex',
        flexDirection: 'column',
        position: 'relative',
      }}
    >
      <div
        onMouseDown={onDragStart}
        title="拖动调整高度"
        style={{
          position: 'absolute',
          top: -4,
          left: 0,
          right: 0,
          height: 8,
          cursor: 'row-resize',
          background: 'transparent',
          zIndex: 10,
        }}
      />
      <Tabs
        size="small"
        activeKey={activeKey}
        onChange={setActiveKey}
        style={{ flex: 1, minHeight: 0, padding: '0 12px' }}
        tabBarStyle={{ marginBottom: 4 }}
        tabBarExtraContent={
          <Button
            type="text"
            size="small"
            icon={<CaretDownOutlined />}
            onClick={() => setDockOpen(false)}
            title="折叠监控"
          />
        }
        items={[
          { key: 'dashboard', label: '大盘', children: <div style={{ overflow: 'auto', height: height - 64 }}><DashboardTab /></div> },
          { key: 'actions', label: '动作', children: <div style={{ overflow: 'auto', height: height - 64 }}><ActionsTab /></div> },
          { key: 'errors', label: '错误', children: <div style={{ overflow: 'auto', height: height - 64 }}><ErrorsTab /></div> },
          { key: 'trends', label: '趋势', children: <div style={{ overflow: 'auto', height: height - 64 }}><TrendsTab /></div> },
          { key: 'per-agent', label: '按节点', children: <div style={{ overflow: 'auto', height: height - 64 }}><PerAgentTab /></div> },
        ]}
      />
    </div>
  );
}
