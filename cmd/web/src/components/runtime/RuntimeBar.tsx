/**
 * 顶部运行控制条：状态徽章 + 启停 + 跨模块入口 + 设置 Popover。
 *
 * 视觉规范：
 *   - 单行布局，分四组：状态 | 主操作 | 跨模块 | 设置；分组用 `Divider type="vertical"`；
 *   - 所有按钮 size middle、图标 + 短中文，与左侧 Toolbar 对齐；
 *   - 设置（主题切换 / 监听边显示）收纳到 Popover，避免顶栏挤太多控件。
 *
 * 模式切换：
 *   - edit：显示集群在线/可用容量；启动按钮高亮；
 *   - running / viewActive：显示任务名 + state 徽章；停止按钮（仅 owned 强提示，viewActive 也可点但服务端会拒）；
 *   - finalReport：banner + 查看历史 / 恢复编辑稿 / 新建任务。
 */

import {
  ApiOutlined,
  BgColorsOutlined,
  BranchesOutlined,
  CloudUploadOutlined,
  DatabaseOutlined,
  HistoryOutlined,
  LinkOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  SettingOutlined,
  StopOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import { App as AntApp, Badge, Button, Divider, Popover, Space, Switch, Tag, Tooltip, Typography } from 'antd';
import { useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import {
  hasStashedDraft,
  restoreStashedDraft,
  showApiError,
  stopTask,
  useRuntimeStore,
} from '@/services';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { TaskStartModal } from './TaskStartModal';
import type { TaskBrief } from '@/types/api';

export interface RuntimeBarProps {
  onOpenResources?: () => void;
  onOpenHistory?: () => void;
  onOpenAgents?: () => void;
  onOpenBinaries?: () => void;
}

const SECTION_DIVIDER = (
  <Divider type="vertical" style={{ margin: '0 6px', height: 22, borderColor: 'rgba(127,127,127,0.18)' }} />
);

const STATE_COLOR: Record<TaskBrief['state'], string> = {
  pending: 'default',
  starting: 'gold',
  running: 'processing',
  stopping: 'volcano',
  stopped: 'default',
  failed: 'error',
};

export function RuntimeBar({
  onOpenResources,
  onOpenHistory,
  onOpenAgents,
  onOpenBinaries,
}: RuntimeBarProps) {
  const { modal } = AntApp.useApp();

  const {
    mode,
    activeTask,
    ownedTaskId,
    agents,
    connectionLost,
    detachFromActive,
    reset,
  } = useRuntimeStore(
    useShallow((s) => ({
      mode: s.mode,
      activeTask: s.activeTask,
      ownedTaskId: s.ownedTaskId,
      agents: s.agents,
      connectionLost: s.connectionLost,
      detachFromActive: s.detachFromActive,
      reset: s.reset,
    })),
  );

  // 设置 Popover 用到的 UI 状态 + 打开协议适配器面板的 setter
  const { theme, setTheme, showListenEdges, toggleListenEdges, setActivePanel } = useEditorStore(
    useShallow((s) => ({
      theme: s.theme,
      setTheme: s.setTheme,
      showListenEdges: s.showListenEdges,
      toggleListenEdges: s.toggleListenEdges,
      setActivePanel: s.setActivePanel,
    })),
  );

  const [startOpen, setStartOpen] = useState(false);
  const [stopping, setStopping] = useState(false);

  const safeAgents = agents ?? [];
  const onlineAgents = safeAgents.filter((a) => a.status !== 'offline').length;
  const unhealthyCount = safeAgents.filter((a) => a.status === 'unhealthy').length;
  const availableBots = safeAgents
    .filter((a) => a.status === 'idle' || a.status === 'busy')
    .reduce((sum, a) => sum + Math.max(0, a.maxBots - a.currentBots), 0);

  const handleStop = (task: TaskBrief) => {
    modal.confirm({
      title: '停止任务？',
      content: `任务 "${task.name}" 将进入 stopping 状态，等待所有 Agent 停止后转为 stopped。`,
      okType: 'danger',
      onOk: async () => {
        setStopping(true);
        try {
          await stopTask(task.id);
        } catch (e) {
          showApiError(e);
        } finally {
          setStopping(false);
        }
      },
    });
  };

  const handleNewTask = () => {
    modal.confirm({
      title: '新建任务？',
      content: '当前画布与运行态数据将被清空，是否继续？',
      onOk: () => reset(),
    });
  };

  const handleRestore = () => {
    if (restoreStashedDraft()) {
      detachFromActive();
    }
  };

  const settingsContent = (
    <Space direction="vertical" size={10} style={{ minWidth: 220 }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
        <Space size={6}>
          <BgColorsOutlined style={{ color: 'var(--text-secondary)' }} />
          <span>主题</span>
        </Space>
        <Switch
          checked={theme === 'dark'}
          onChange={(v) => setTheme(v ? 'dark' : 'light')}
          checkedChildren="深色"
          unCheckedChildren="浅色"
        />
      </div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
        <Space size={6}>
          <BranchesOutlined style={{ color: 'var(--text-secondary)' }} />
          <span>显示监听边</span>
        </Space>
        <Switch checked={showListenEdges} onChange={() => toggleListenEdges()} size="small" />
      </div>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        action 与 CallbackCard 之间的虚线
      </Typography.Text>
    </Space>
  );

  return (
    <Space size={4} align="center">
      {connectionLost && <Tag color="error">Admin 连接异常</Tag>}

      {/* === 状态徽章组 === */}
      {mode === 'edit' && (
        <Tooltip title={`在线 Agent ${onlineAgents}/${safeAgents.length} · 可用容量 ${availableBots}`}>
          <Tag
            icon={<ThunderboltOutlined />}
            color={onlineAgents > 0 ? 'green' : 'default'}
            style={{ margin: 0 }}
          >
            集群 {onlineAgents}/{safeAgents.length} · 可用 {availableBots}
          </Tag>
        </Tooltip>
      )}
      {(mode === 'running' || mode === 'viewActive') && activeTask && (
        <Space size={4}>
          <Tag color={STATE_COLOR[activeTask.state]} style={{ margin: 0 }}>
            {activeTask.state}
          </Tag>
          <Typography.Text style={{ maxWidth: 160 }} ellipsis title={activeTask.name}>
            {activeTask.name}
          </Typography.Text>
        </Space>
      )}
      {mode === 'finalReport' && (
        <Tag color={activeTask?.state === 'failed' ? 'error' : 'default'} style={{ margin: 0 }}>
          已结束（{activeTask?.state ?? 'stopped'}）
        </Tag>
      )}

      {SECTION_DIVIDER}

      {/* === 主操作组 === */}
      {mode === 'edit' && (
        <Tooltip title={onlineAgents === 0 ? '没有在线的 Agent，无法启动' : ''}>
          <Button
            type="primary"
            icon={<PlayCircleOutlined />}
            onClick={() => setStartOpen(true)}
            disabled={onlineAgents === 0}
          >
            启动
          </Button>
        </Tooltip>
      )}
      {(mode === 'running' || mode === 'viewActive') && activeTask && (
        <Tooltip
          title={
            mode === 'viewActive' && ownedTaskId !== activeTask.id
              ? '该任务由其他客户端发起；服务端允许任意客户端停止'
              : ''
          }
        >
          <Button
            danger
            icon={<StopOutlined />}
            loading={stopping}
            onClick={() => handleStop(activeTask)}
          >
            停止
          </Button>
        </Tooltip>
      )}
      {mode === 'finalReport' && (
        <Space.Compact>
          <Tooltip title="查看本次任务的归档详情">
            <Button icon={<HistoryOutlined />} onClick={onOpenHistory}>
              查看历史
            </Button>
          </Tooltip>
          <Tooltip title={hasStashedDraft() ? '从 LocalStorage 还原进入运行前的本地稿' : '没有暂存的本地稿'}>
            <Button disabled={!hasStashedDraft()} onClick={handleRestore}>
              恢复编辑稿
            </Button>
          </Tooltip>
          <Tooltip title="清空当前画布与运行态">
            <Button icon={<ReloadOutlined />} onClick={handleNewTask}>
              新建
            </Button>
          </Tooltip>
        </Space.Compact>
      )}

      {SECTION_DIVIDER}

      {/* === 跨模块入口组（统一图标 + 文字） === */}
      {/* 适配器在最左：与"资源"同属协议/资源准备类（codec.lua 也是 lua 脚本），仅 edit 态可打开 */}
      <Space size={4}>
        <Tooltip title="协议适配器（codec.lua）— 通用游戏服务器协议接入">
          <Button
            icon={<LinkOutlined />}
            onClick={() => setActivePanel({ kind: 'codecAdapter' })}
            disabled={mode !== 'edit'}
          >
            适配器
          </Button>
        </Tooltip>
        <Tooltip title="资源管理（proto / lua 文件）">
          <Button icon={<DatabaseOutlined />} onClick={onOpenResources}>
            资源
          </Button>
        </Tooltip>
        <Tooltip title="历史压测记录">
          <Button icon={<HistoryOutlined />} onClick={onOpenHistory}>
            历史
          </Button>
        </Tooltip>
        <Tooltip title="Agent 节点管理">
          <Badge count={unhealthyCount} size="small" offset={[-4, 4]}>
            <Button icon={<ApiOutlined />} onClick={onOpenAgents}>
              节点
            </Button>
          </Badge>
        </Tooltip>
        <Tooltip title="二进制 / 升级管理">
          <Button icon={<CloudUploadOutlined />} onClick={onOpenBinaries}>
            升级
          </Button>
        </Tooltip>
      </Space>

      {SECTION_DIVIDER}

      {/* === 设置 Popover === */}
      <Popover
        content={settingsContent}
        title="界面设置"
        trigger="click"
        placement="bottomRight"
      >
        <Tooltip title="主题 / 监听边显示">
          <Button icon={<SettingOutlined />} />
        </Tooltip>
      </Popover>

      <TaskStartModal open={startOpen} onClose={() => setStartOpen(false)} />
    </Space>
  );
}
