/**
 * 顶部运行控制条：状态徽章 + 启停 + 跨模块入口 + 设置 Popover。
 *
 * 视觉规范：
 *   - 单行布局，分四组：状态 | 主操作 | 跨模块 | 设置；分组用 `Divider type="vertical"`；
 *   - 所有按钮 size middle、图标 + 短中文，与左侧 Toolbar 对齐；
 *   - 设置（主题切换 / 监听边显示）收纳到 Popover，避免顶栏挤太多控件。
 *
 * 模式切换：
 *   - edit：显示集群在线/总容量；启动按钮高亮；
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
  ClusterOutlined,
  DatabaseOutlined,
  EditOutlined,
  FileTextOutlined,
  HistoryOutlined,
  EyeOutlined,
  PlayCircleOutlined,
  SettingOutlined,
  StopOutlined,
  ThunderboltOutlined,
  DashboardOutlined,
  AlignLeftOutlined,
} from '@ant-design/icons';
import { App as AntApp, Badge, Button, Divider, Popover, Segmented, Space, Switch, Tag, Tooltip, Typography } from 'antd';
import { useMemo, useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import {
  attachToActive,
  detachToEditWithRestore,
  detachFromActiveWithRestore,
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
  onOpenSystem?: () => void;
  onOpenLogs?: () => void;
  onOpenNotepad?: () => void;
  onOpenProtocolConfig?: () => void;
}

const SECTION_DIVIDER = (
  <Divider type="vertical" style={{ margin: '0 6px', height: 22, borderColor: 'var(--divider-bg)' }} />
);

const STATE_COLOR: Record<TaskBrief['state'], string> = {
  pending: 'default',
  starting: 'gold',
  running: 'processing',
  stopping: 'volcano',
  stopped: 'success',
  failed: 'error',
};

const STATE_LABEL: Record<TaskBrief['state'], string> = {
  pending: '待启动',
  starting: '启动中',
  running: '运行中',
  stopping: '停止中',
  stopped: '已停止',
  failed: '失败',
};

export function RuntimeBar({
  onOpenResources,
  onOpenHistory,
  onOpenAgents,
  onOpenSystem,
  onOpenLogs,
  onOpenNotepad,
  onOpenProtocolConfig,
}: RuntimeBarProps) {
  const { modal } = AntApp.useApp();

  const {
    mode,
    activeTask,
    detachedActiveTask,
    ownedTaskId,
    agents,
    connectionLost,
    agentEvents,
  } = useRuntimeStore(
    useShallow((s) => ({
      mode: s.mode,
      activeTask: s.activeTask,
      detachedActiveTask: s.detachedActiveTask,
      ownedTaskId: s.ownedTaskId,
      agents: s.agents,
      connectionLost: s.connectionLost,
      agentEvents: s.agentEvents,
    })),
  );

  // 设置 Popover 用到的 UI 状态
  const { theme, setTheme, showListenEdges, toggleListenEdges, debugMode, setDebugMode, historyEnabled, pendingSyncResult, codecSchemaErrors } = useEditorStore(
    useShallow((s) => ({
      theme: s.theme,
      setTheme: s.setTheme,
      showListenEdges: s.showListenEdges,
      toggleListenEdges: s.toggleListenEdges,
      debugMode: s.debugMode,
      setDebugMode: s.setDebugMode,
      historyEnabled: s.historyEnabled,
      pendingSyncResult: s.pendingSyncResult,
      codecSchemaErrors: s.codecSchemaErrors,
    })),
  );

  const [startOpen, setStartOpen] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [attaching, setAttaching] = useState(false);

  const safeAgents = agents ?? [];
  const onlineAgents = safeAgents.filter((a) => a.status !== 'offline').length;
  const totalCapacity = safeAgents
    .filter((a) => a.status !== 'offline')
    .reduce((sum, a) => sum + a.maxBots, 0);
  const startDisabled = onlineAgents === 0;
  const startDisabledTip = onlineAgents === 0 ? '没有在线的节点，无法启动' : '';
  const modeIcon = debugMode ? <BugOutlined /> : <CheckCircleOutlined />;
  const modeText = debugMode ? '调试' : '测试';
  const modeColor = debugMode ? 'var(--mode-debug-color)' : 'var(--mode-test-color)';
  const modeTip = debugMode
    ? '调试模式：启动跳过容量预检 + 自动装填 1 个机器人 / 并发 1 + 日志级别 debug。点击"设置"可切换到测试模式'
    : '测试模式：使用你填写的完整配置，启用容量预检与默认日志级别。点击"设置"可切换到调试模式';
  const modeTagStyle = {
    margin: 0,
    color: modeColor,
    borderColor: `color-mix(in srgb, ${modeColor} 32%, transparent)`,
    background: `color-mix(in srgb, ${modeColor} 10%, transparent)`,
  };
  const startButtonColor = modeColor;
  const startButtonStyle = useMemo(
    () => ({
      background: startButtonColor,
      borderColor: startButtonColor,
      boxShadow: `0 4px 12px color-mix(in srgb, ${startButtonColor} 28%, transparent)`,
    }),
    [startButtonColor],
  );

  const handleStop = (task: TaskBrief) => {
    modal.confirm({
      title: '停止任务？',
      content: `任务 "${task.name}" 将进入停止中状态，等待所有节点完成后转为已停止`,
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

  const handleViewMonitor = async () => {
    if (!detachedActiveTask) return;
    setAttaching(true);
    try {
      await attachToActive(detachedActiveTask.id);
    } catch (e) {
      showApiError(e);
    } finally {
      setAttaching(false);
    }
  };

  // 「返回编辑」：恢复本地草稿画布 + 切换状态机。
  // finalReport 下清除 stash 并退出任务上下文。
  const handleBackToEdit = () => {
    detachFromActiveWithRestore();
  };

  const modeTag = (
    <Tooltip title={modeTip}>
      <Tag icon={modeIcon} style={modeTagStyle}>
        {modeText}
      </Tag>
    </Tooltip>
  );

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
        动作节点与监听之间的虚线
      </Typography.Text>
      {/* 运行模式：测试 ↔ 调试 二选一。
          - 测试（默认）：用户填写的完整配置 + 启用容量预检 + 默认日志级别；
          - 调试：自动装填 1 个机器人/并发 1 + 跳过容量预检 + log=debug。
          颜色与 RuntimeBar 状态徽章 / TaskStartModal 中的提示一致：
          调试=紫色（BugOutlined），测试=蓝色（CheckCircleOutlined）。 */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <Space size={6}>
          {debugMode ? (
            <BugOutlined style={{ color: 'var(--mode-debug-color)' }} />
          ) : (
            <CheckCircleOutlined style={{ color: 'var(--mode-test-color)' }} />
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
                <span style={{ color: !debugMode ? 'var(--mode-test-color)' : undefined, fontWeight: !debugMode ? 600 : undefined }}>
                  <CheckCircleOutlined style={{ marginRight: 4 }} />
                  测试
                </span>
              ),
              value: 'test',
            },
            {
              label: (
                <span style={{ color: debugMode ? 'var(--mode-debug-color)' : undefined, fontWeight: debugMode ? 600 : undefined }}>
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
      {connectionLost && <Tag color="error">服务器连接异常</Tag>}

      {/* === 状态徽章组 === */}
      {mode === 'edit' && (
        <Tooltip title={`在线节点 ${onlineAgents} · 总容量 ${totalCapacity}`}>
          <Tag
            icon={<ThunderboltOutlined />}
            color={onlineAgents > 0 ? 'green' : 'default'}
            style={{ margin: 0 }}
          >
            在线 {onlineAgents} · 总容量 {totalCapacity}
          </Tag>
        </Tooltip>
      )}
      {mode === 'edit' && detachedActiveTask && (
        <Space size={4}>
          <Tag color={STATE_COLOR[detachedActiveTask.state]} style={{ margin: 0 }}>
            {STATE_LABEL[detachedActiveTask.state]}
          </Tag>
          {modeTag}
        </Space>
      )}
      {mode === 'edit' && !detachedActiveTask && modeTag}
      {(mode === 'running' || mode === 'viewActive') && activeTask && (
        <Space size={4}>
          <Tag color={STATE_COLOR[activeTask.state]} style={{ margin: 0 }}>
            {STATE_LABEL[activeTask.state]}
          </Tag>
          {modeTag}
          {agentEvents.some((e) => e.type === 'offline' || e.type === 'restarted') && (() => {
            const offlineIds = new Set(
              agentEvents.filter((e) => e.type === 'offline').map((e) => e.agentId),
            );
            const restartedIds = new Set(
              agentEvents.filter((e) => e.type === 'restarted').map((e) => e.agentId),
            );
            const reconnectedIds = new Set(
              agentEvents.filter((e) => e.type === 'reconnected').map((e) => e.agentId),
            );
            const stillOffline = [...offlineIds].filter((id) => !reconnectedIds.has(id));
            const restartedOnly = [...restartedIds].filter((id) => !stillOffline.includes(id));
            const problems = [...stillOffline, ...restartedOnly];
            return problems.length > 0 ? (
              <Tooltip
                title={
                  <>
                    {stillOffline.length > 0 && <div>离线节点：{stillOffline.join('、')}</div>}
                    {restartedOnly.length > 0 && <div>重启丢任务：{restartedOnly.join('、')}</div>}
                  </>
                }
              >
                <Tag color="warning" style={{ margin: 0 }}>
                  {problems.length} 节点异常
                </Tag>
              </Tooltip>
            ) : null;
          })()}
        </Space>
      )}
      {mode === 'finalReport' && (
        <Space size={4}>
          <Tag color={STATE_COLOR[activeTask?.state ?? 'stopped']} style={{ margin: 0 }}>
            {STATE_LABEL[activeTask?.state ?? 'stopped']}
          </Tag>
          {(() => {
            const detail = activeTask as { cleanupSummary?: import('@/types/api').CleanupStatus } | null;
            const summary = detail?.cleanupSummary;
            if (!summary || summary.status === 'ok') return null;
            const map: Record<string, { color: string; label: string }> = {
              partial: { color: 'orange', label: '部分清理' },
              timeout: { color: 'red', label: '清理超时' },
              unknown: { color: 'default', label: '清理未知' },
            };
            const info = map[summary.status] ?? { color: 'orange', label: '清理异常' };
            const detailLines: string[] = [];
            if (summary.message) detailLines.push(summary.message);
            if (summary.timeoutRobots) detailLines.push(`超时机器人 ${summary.timeoutRobots}`);
            if (summary.luaSkipped) detailLines.push(`脚本运行时未归还 ${summary.luaSkipped}`);
            return (
              <Tooltip title={detailLines.join('；') || info.label}>
                <Tag color={info.color} style={{ margin: 0 }}>{info.label}</Tag>
              </Tooltip>
            );
          })()}
        </Space>
      )}

      {SECTION_DIVIDER}

      {/* === 主操作组 === */}
      {mode === 'edit' && (
        <Space size={4}>
          {detachedActiveTask && (
            <Tooltip title="进入当前运行任务的监控面板，画布将切换为只读">
              <Button
                type="primary"
                icon={<EyeOutlined />}
                loading={attaching}
                onClick={handleViewMonitor}
                style={startButtonStyle}
              >
                监测
              </Button>
            </Tooltip>
          )}
          {!detachedActiveTask && (
            <Tooltip title={startDisabledTip}>
              <Button
                type="primary"
                icon={<PlayCircleOutlined />}
                onClick={() => setStartOpen(true)}
                disabled={startDisabled}
                style={startDisabled ? undefined : startButtonStyle}
              >
                启动
              </Button>
            </Tooltip>
          )}
        </Space>
      )}
      {(mode === 'running' || mode === 'viewActive') && activeTask && (
        <Space size={4}>
          <Button type="primary" icon={<EditOutlined />} onClick={detachToEditWithRestore} style={startButtonStyle}>
            返回编辑
          </Button>
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
        </Space>
      )}
      {mode === 'finalReport' && (
        <Tooltip title="退出监控状态，画布与最后一份监控数据保留，可继续修改流程">
          <Button type="primary" icon={<EditOutlined />} onClick={handleBackToEdit}>
            返回编辑
          </Button>
        </Tooltip>
      )}

      {SECTION_DIVIDER}

      {/* === 跨模块入口组（统一图标 + 文字） === */}
      <Space size={4}>
        <Tooltip title="系统状态：查看服务器与各节点硬件级指标">
          <Button icon={<DashboardOutlined />} onClick={onOpenSystem}>
            系统
          </Button>
        </Tooltip>
        <Tooltip title="运行日志：查看服务器与节点输出的文本日志">
          <Button icon={<AlignLeftOutlined />} onClick={onOpenLogs}>
            日志
          </Button>
        </Tooltip>
        <Tooltip title="记事本：编辑笔记、导入定义文件、快速查找路由">
          <Button icon={<FileTextOutlined />} onClick={onOpenNotepad}>
            记事本
          </Button>
        </Tooltip>
        <Tooltip title={codecSchemaErrors && codecSchemaErrors.length > 0 ? `协议配置有 ${codecSchemaErrors.length} 处问题` : '协议配置：按连接管理帧布局与错误码映射'}>
          <Badge
            count={codecSchemaErrors && codecSchemaErrors.length > 0 ? codecSchemaErrors.length : 0}
            overflowCount={99}
            offset={[-4, 4]}
          >
            <Button icon={<ClusterOutlined />} onClick={onOpenProtocolConfig}>
              协议
            </Button>
          </Badge>
        </Tooltip>
        <Tooltip title="资源管理：编辑定义文件与脚本，并在启动任务时下发到节点">
          <Badge
            count={pendingSyncResult ? pendingSyncResult.conflicts.length + pendingSyncResult.removed.length : 0}
            overflowCount={99}
            offset={[-4, 4]}
            color="orange"
          >
            <Button icon={<DatabaseOutlined />} onClick={onOpenResources}>
              资源
            </Button>
          </Badge>
        </Tooltip>
        <Tooltip
          title={
            historyEnabled === false
              ? '历史压测记录（历史记录功能未启用，请检查服务器配置）'
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
        <Tooltip title="节点管理">
          <Button icon={<ApiOutlined />} onClick={onOpenAgents}>
            节点
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
