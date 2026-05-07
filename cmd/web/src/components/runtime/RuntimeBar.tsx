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
 *   - finalReport：banner + 「返回编辑」（主操作）+ 「恢复编辑稿」（仅有 stash 时显示）。
 *     - 「返回编辑」detachFromActive() → mode='edit'，画布与最后一份监控快照原样保留；
 *     - 「恢复编辑稿」仅在 attachToActive 路径写过 stash 时才出现，避免空按钮误导；
 *     - 「查看历史」「新建」不在这里显示——前者与右侧"历史"按钮重复且 HISTORY_DISABLED
 *       时会报错，后者属于低频清空操作，可在 edit 模式下走通用流程。
 */

import {
  ApiOutlined,
  BgColorsOutlined,
  BranchesOutlined,
  BugOutlined,
  CheckCircleOutlined,
  DatabaseOutlined,
  EditOutlined,
  HistoryOutlined,
  LinkOutlined,
  PlayCircleOutlined,
  SettingOutlined,
  StopOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import { App as AntApp, Badge, Button, Divider, Popover, Segmented, Space, Switch, Tag, Tooltip, Typography } from 'antd';
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
}: RuntimeBarProps) {
  const { modal } = AntApp.useApp();

  const {
    mode,
    activeTask,
    ownedTaskId,
    agents,
    connectionLost,
    detachFromActive,
  } = useRuntimeStore(
    useShallow((s) => ({
      mode: s.mode,
      activeTask: s.activeTask,
      ownedTaskId: s.ownedTaskId,
      agents: s.agents,
      connectionLost: s.connectionLost,
      detachFromActive: s.detachFromActive,
    })),
  );

  // 设置 Popover 用到的 UI 状态 + 打开协议适配器面板的 setter
  const { theme, setTheme, showListenEdges, toggleListenEdges, setActivePanel, debugMode, setDebugMode, historyEnabled } = useEditorStore(
    useShallow((s) => ({
      theme: s.theme,
      setTheme: s.setTheme,
      showListenEdges: s.showListenEdges,
      toggleListenEdges: s.toggleListenEdges,
      setActivePanel: s.setActivePanel,
      debugMode: s.debugMode,
      setDebugMode: s.setDebugMode,
      historyEnabled: s.historyEnabled,
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

  const handleRestore = () => {
    if (restoreStashedDraft()) {
      detachFromActive();
    }
  };

  // 「返回编辑」：纯状态机切换，不动画布也不动监控数据。
  // 适用场景：自己启动的任务结束后想接着改流程；attach 别人的任务结束后不需要恢复 stash。
  // 与 reset() 的区别：reset 会清空画布、表单与监控；这里只负责退出"只读 + 监控"。
  const handleBackToEdit = () => {
    detachFromActive();
  };

  const settingsContent = (
    <Space direction="vertical" size={10} style={{ minWidth: 240 }}>
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
      {/* 运行模式：测试 ↔ 调试 二选一。
          - 测试（默认）：用户填写的完整配置 + 启用容量预检 + 默认日志级别；
          - 调试：自动装填 1 个机器人/并发 1 + 跳过容量预检 + log=debug。
          颜色与 RuntimeBar 状态徽章 / TaskStartModal 中的提示一致：
          调试=紫色（BugOutlined），测试=蓝色（CheckCircleOutlined）。 */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <Space size={6}>
          {debugMode ? (
            <BugOutlined style={{ color: '#9254de' }} />
          ) : (
            <CheckCircleOutlined style={{ color: '#1677ff' }} />
          )}
          <span>运行模式</span>
        </Space>
        <Segmented
          size="small"
          block
          value={debugMode ? 'debug' : 'test'}
          onChange={(v) => setDebugMode(v === 'debug')}
          options={[
            {
              label: (
                <span style={{ color: !debugMode ? '#1677ff' : undefined, fontWeight: !debugMode ? 600 : undefined }}>
                  <CheckCircleOutlined style={{ marginRight: 4 }} />
                  测试
                </span>
              ),
              value: 'test',
            },
            {
              label: (
                <span style={{ color: debugMode ? '#9254de' : undefined, fontWeight: debugMode ? 600 : undefined }}>
                  <BugOutlined style={{ marginRight: 4 }} />
                  调试
                </span>
              ),
              value: 'debug',
            },
          ]}
        />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {debugMode
            ? '调试：自动装填 1 个机器人 / 并发 1，跳过容量预检，日志级别 debug'
            : '测试：使用你填写的完整配置，启用容量预检与默认日志级别'}
        </Typography.Text>
      </div>
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
      {/* 模式标识：测试 vs 调试 互斥
          - debugMode=true  → 紫色"调试"标签：装填小数据 + 跳过容量预检 + log=debug；
          - debugMode=false → 蓝色"测试"标签（默认）：保留用户填写的全量配置，按容量预检与日志级别。
          颜色与 设置 Popover / TaskStartModal 中保持一致，让用户一眼看到当前是哪种模式。 */}
      {mode === 'edit' && (
        <Tooltip
          title={
            debugMode
              ? '调试模式：启动跳过容量预检 + 自动装填 1 个机器人 / 并发 1 + 日志级别 debug。点击"设置"可切换到测试模式'
              : '测试模式：使用你填写的完整配置，启用容量预检与默认日志级别。点击"设置"可切换到调试模式'
          }
        >
          {debugMode ? (
            <Tag icon={<BugOutlined />} color="purple" style={{ margin: 0 }}>
              调试
            </Tag>
          ) : (
            <Tag icon={<CheckCircleOutlined />} color="blue" style={{ margin: 0 }}>
              测试
            </Tag>
          )}
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
        <Space size={4}>
          <Tooltip title="退出监控状态，画布与最后一份监控数据保留，可继续修改流程">
            <Button type="primary" icon={<EditOutlined />} onClick={handleBackToEdit}>
              返回编辑
            </Button>
          </Tooltip>
          {hasStashedDraft() && (
            <Tooltip title="从 LocalStorage 还原「查看运行中」之前缓存的本地稿">
              <Button onClick={handleRestore}>恢复编辑稿</Button>
            </Tooltip>
          )}
        </Space>
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
        <Tooltip
          title={
            historyEnabled === false
              ? '历史压测记录（admin 未启用 history 模块，请配置 MySQL）'
              : '历史压测记录'
          }
        >
          {/* 历史模块需要 admin 配 MySQL；未启用时禁用按钮，避免点完才弹 HISTORY_DISABLED。
              historyEnabled === null 表示尚未探测完，不主动禁用，避免冷启动期间整列灰着。 */}
          <Button
            icon={<HistoryOutlined />}
            onClick={onOpenHistory}
            disabled={historyEnabled === false}
          >
            历史
          </Button>
        </Tooltip>
        <Tooltip title="Agent 节点管理 / 批量停止">
          <Badge count={unhealthyCount} size="small" offset={[-4, 4]}>
            <Button icon={<ApiOutlined />} onClick={onOpenAgents}>
              节点
            </Button>
          </Badge>
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
