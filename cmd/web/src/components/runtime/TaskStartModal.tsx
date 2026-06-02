/**
 * 启动任务确认弹窗：参数复核 + 资源清单 + 容量预检 + 提交。
 *
 * 数据来源：
 *   - 任务名 / totalBots / robotConfig / deadline 来自 useRuntimeStore（与 RuntimeBar 双向绑定）；
 *   - 资源清单（proto / lua）来自 resourcesStore.listProto / listScript；
 *   - 容量提示来自 useRuntimeStore.agents。
 *
 * 运行模式（editorStore.debugMode）：测试 ↔ 调试 互斥，顶部 Segmented 控制，持久化到 localStorage。
 *   - 测试（debugMode=false，默认，蓝色）：使用用户填写的全量配置 + 容量预检 + 默认日志级别；
 *   - 调试（debugMode=true，紫色）：自动装填 totalBots=1 / concurrency=1 / logLevel=debug，
 *     后端 debugMode=true 时单 Agent 分配 + 历史自动标记 "debug"；
 *   - 0 个在线 Agent 时无论哪种模式都 disable 启动按钮（前端最低门槛）；
 *   - 模式切换不回滚已填数值（保留用户偏好）。
 *
 * 日志等级（robotConfig.logLevel）：
 *   - 任务期临时切换 Agent 进程的日志等级，任务结束后 Agent 自动恢复原等级；
 *   - debug 等级下会打印全部收发包与字段绑定，仅用于调试，测试压测建议 info。
 *
 * 提交：调用 services.startTask；成功后由调用方关闭 modal，失败由 showApiError 接住并展示。
 */

import {
  Alert,
  Button,
  Collapse,
  DatePicker,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Modal,
  Segmented,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd';
import type { LogLevel, RampUpStage } from '@/types/api';
import { AuthExtraEditor } from './AuthExtraEditor';
import { BugOutlined, CheckCircleOutlined, DeleteOutlined, PlusOutlined, ThunderboltOutlined } from '@ant-design/icons';
import { useEffect, useMemo, useRef, useState } from 'react';
import dayjs, { type Dayjs } from 'dayjs';
import { useShallow } from 'zustand/react/shallow';
import { showApiError, startTask, useRuntimeStore } from '@/services';
import { hasSyncDiff, listProto, listScript, subtractSyncResult, type BaselineSyncResult, type ResourceFile } from '@/services/resourcesStore';
import { checkTaskResourcesAgainstBaseline } from '@/services/taskResourceDiff';
import { syncFlowScriptsToIdb, collectFlowScriptNames } from '@/services/scriptSync';
import { useFlowStore } from '@/components/FlowEditor/store/flowStore';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { useFloatingWindowStore } from '@/components/FlowEditor/store/floatingWindowStore';
import { BaselineSyncModal } from '@/components/modules/BaselineSyncModal';

export interface TaskStartModalProps {
  open: boolean;
  onClose: () => void;
  /** 启动成功后的回调（拿到 taskId） */
  onStarted?: (taskId: string) => void;
}

/** 调试模式装填一次性使用的预设值。统一在此声明便于后续调整。 */
const DEBUG_PRESET = {
  totalBots: 1,
  concurrency: 1,
  logLevel: 'debug' as LogLevel,
} as const;

/**
 * 日志等级选项。
 *
 * - debug：所有动作收发包 / 字段绑定 / 心跳过程都会打印，开发调试用；
 * - info ：默认等级，关键节点（连接、登录、阶段切换、任务起停）；
 * - warn ：仅异常或潜在问题；
 * - error：仅错误。
 *
 * 该字段会临时改写 Agent 进程的日志等级，任务结束后自动恢复，不影响其他任务。
 */
const LOG_LEVEL_OPTIONS: Array<{ value: LogLevel; label: string; desc: string }> = [
  { value: 'debug', label: 'debug', desc: '全量收发包/字段，调试用' },
  { value: 'info', label: 'info', desc: '默认；关键节点' },
  { value: 'warn', label: 'warn', desc: '仅警告' },
  { value: 'error', label: 'error', desc: '仅错误' },
];

export function TaskStartModal({ open, onClose, onStarted }: TaskStartModalProps) {
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
  const {
    taskName,
    totalBots,
    robotConfig,
    deadline,
    agents,
    rampUpEnabled,
    rampUpStages,
    setTaskName,
    setTotalBots,
    setRobotConfig,
    setDeadline,
    setRampUpEnabled,
    setRampUpStages,
  } = useRuntimeStore(
    useShallow((s) => ({
      taskName: s.taskName,
      totalBots: s.totalBots,
      robotConfig: s.robotConfig,
      deadline: s.deadline,
      agents: s.agents,
      rampUpEnabled: s.rampUpEnabled,
      rampUpStages: s.rampUpStages,
      setTaskName: s.setTaskName,
      setTotalBots: s.setTotalBots,
      setRobotConfig: s.setRobotConfig,
      setDeadline: s.setDeadline,
      setRampUpEnabled: s.setRampUpEnabled,
      setRampUpStages: s.setRampUpStages,
    })),
  );

  const { debugMode, setDebugMode, advancedExpanded, setAdvancedExpanded } = useEditorStore(
    useShallow((s) => ({
      debugMode: s.debugMode,
      setDebugMode: s.setDebugMode,
      advancedExpanded: s.taskFormAdvancedExpanded,
      setAdvancedExpanded: s.setTaskFormAdvancedExpanded,
    })),
  );

  const [protos, setProtos] = useState<ResourceFile[]>([]);
  const [scripts, setScripts] = useState<ResourceFile[]>([]);
  const [submitting, setSubmitting] = useState(false);
  /** flow 引用的脚本总数（actions/callbacks 中 script 字段的去重和） */
  const [refScriptCount, setRefScriptCount] = useState(0);
  /** flow 引用了但既不在 IDB 也拉不到默认基线的脚本名（启动会失败） */
  const [missingScripts, setMissingScripts] = useState<string[]>([]);
  /** 资源同步进行中，给 UI 一个轻量 loading 态 */
  const [syncing, setSyncing] = useState(false);
  const [taskDiffResult, setTaskDiffResult] = useState<BaselineSyncResult | null>(null);
  const [diffChoiceOpen, setDiffChoiceOpen] = useState(false);
  const [diffResolveOpen, setDiffResolveOpen] = useState(false);

  // 弹窗打开 → 基线资源全量对比 + flow 引用脚本 gap-fill + 收集 IDB 全集
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setSyncing(true);
    (async () => {
      try {
        const flow = useFlowStore.getState().toTaskFlow();
        const refNames = collectFlowScriptNames(flow);
        // flow 引用脚本 gap-fill
        const scriptSync = await syncFlowScriptsToIdb(flow);
        // 收集 IDB 全集
        const [p, s] = await Promise.all([listProto(), listScript()]);
        if (cancelled) return;
        setProtos(p);
        setScripts(s);
        setRefScriptCount(refNames.length);
        setMissingScripts(scriptSync.missing);
      } finally {
        if (!cancelled) setSyncing(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [open]);

  // 弹窗打开时自动生成任务名 + 调试模式装填预设值。
  const filledRef = useRef(false);
  useEffect(() => {
    if (!open) {
      filledRef.current = false;
      return;
    }
    // 自动生成任务名
    const prefix = debugMode ? 'debug' : 'test';
    setTaskName(`${prefix}-${dayjs().format('MMDD-HHmm')}`);
    // 调试模式装填预设值
    if (debugMode && !filledRef.current) {
      setTotalBots(DEBUG_PRESET.totalBots);
      setRobotConfig({
        concurrency: DEBUG_PRESET.concurrency,
        logLevel: DEBUG_PRESET.logLevel,
      });
      filledRef.current = true;
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const totalCapacity = useMemo(() => {
    return (agents ?? [])
      .filter((a) => a.status !== 'offline')
      .reduce((sum, a) => sum + a.maxBots, 0);
  }, [agents]);

  const onlineAgents = (agents ?? []).filter((a) => a.status !== 'offline').length;

  // 渐进式加压：阶段 count 之和
  const rampUpSum = useMemo(
    () => rampUpStages.reduce((s, st) => s + (st.count || 0), 0),
    [rampUpStages],
  );

  // 渐进式加压：计算峰值并发数（考虑 reset）
  // reset=true 的阶段开始前会清空已有机器人，所以峰值是各阶段的最大瞬时并发
  const peakBots = useMemo(() => {
    if (!rampUpEnabled) return totalBots;
    let running = 0;
    let peak = 0;
    for (const st of rampUpStages) {
      if (st.reset) {
        running = 0; // 清空后从零开始
      }
      running += st.count || 0;
      if (running > peak) peak = running;
    }
    return peak;
  }, [rampUpEnabled, rampUpStages, totalBots]);

  // 渐进式加压开启时，totalBots 由阶段累加自动推导，只需检查每阶段 count > 0
  const rampUpValid = !rampUpEnabled || (rampUpStages.length > 0 && rampUpSum > 0 && rampUpStages.every((st) => st.count > 0));

  // 容量预检：用峰值并发数而非总和，因为 reset 阶段不会叠加
  const capacityWarn = !debugMode && peakBots > totalCapacity;
  const noAgentBlock = onlineAgents === 0; // 无 Agent 在线连调试也跑不起来，仍禁用启动

  function onToggleDebug(v: boolean) {
    setDebugMode(v);
    // 切换模式时刷新任务名
    const prefix = v ? 'debug' : 'test';
    setTaskName(`${prefix}-${dayjs().format('MMDD-HHmm')}`);
    if (v) {
      setTotalBots(DEBUG_PRESET.totalBots);
      setRobotConfig({
        concurrency: DEBUG_PRESET.concurrency,
        logLevel: DEBUG_PRESET.logLevel,
      });
      setRampUpEnabled(false);
      filledRef.current = true;
    }
  }

  const executeStart = async (handledDiff?: BaselineSyncResult | null) => {
    const id = await startTask({
      name: taskName,
      totalBots: rampUpEnabled ? rampUpSum : totalBots,
      robotConfig: {
        ...robotConfig,
        debugMode,
        rampUp: rampUpEnabled ? { stages: rampUpStages } : undefined,
      },
      deadline: deadline ?? undefined,
    });
    if (handledDiff) {
      const { pendingSyncResult, setPendingSyncResult } = useEditorStore.getState();
      setPendingSyncResult(subtractSyncResult(pendingSyncResult, handledDiff));
    }
    onStarted?.(id);
    onClose();
  };

  const handleSubmit = async () => {
    setSubmitting(true);
    try {
      const flow = useFlowStore.getState().toTaskFlow();
      const scriptSync = await syncFlowScriptsToIdb(flow);
      setMissingScripts(scriptSync.missing);
      if (scriptSync.missing.length > 0) {
        throw new Error(`缺少脚本：${scriptSync.missing.join(', ')}。请在资源管理上传，或在动作编辑器中直接编写。`);
      }
      const diff = await checkTaskResourcesAgainstBaseline(flow);
      if (hasSyncDiff(diff)) {
        setTaskDiffResult(diff);
        setDiffChoiceOpen(true);
        return;
      }
      await executeStart();
    } catch (e) {
      showApiError(e);
    } finally {
      setSubmitting(false);
    }
  };

  const handleOverwriteRun = async () => {
    if (!taskDiffResult) return;
    setDiffChoiceOpen(false);
    setSubmitting(true);
    try {
      await executeStart(taskDiffResult);
    } catch (e) {
      showApiError(e);
    } finally {
      setSubmitting(false);
    }
  };

  const handleDiffResolved = async () => {
    const handled = taskDiffResult;
    setDiffResolveOpen(false);
    setSubmitting(true);
    try {
      await executeStart(handled);
    } catch (e) {
      showApiError(e);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <>
      <Modal
      title={
        <Space size={8}>
          <span>启动压测任务</span>
          {/* 顶部模式标签：与 RuntimeBar / 设置 Popover 配色一致——
              调试 = 紫色 BugOutlined；测试 = 蓝色 CheckCircleOutlined。 */}
          {debugMode ? (
            <Tag icon={<BugOutlined />} color="purple" style={{ margin: 0 }}>
              调试
            </Tag>
          ) : (
            <Tag icon={<CheckCircleOutlined />} color="blue" style={{ margin: 0 }}>
              测试
            </Tag>
          )}
        </Space>
      }
      open={open}
      onCancel={onClose}
      onOk={handleSubmit}
      okText={debugMode ? '启动（调试）' : '启动（测试）'}
      cancelText="取消"
      confirmLoading={submitting}
      okButtonProps={{
        disabled:
          capacityWarn ||
          noAgentBlock ||
          missingScripts.length > 0 ||
          syncing ||
          !taskName.trim() ||
          peakBots <= 0 ||
          !rampUpValid,
        danger: debugMode ? false : undefined,
      }}
      width={620}
      destroyOnHidden
      styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
    >
      {/* 模式选择条：测试 ↔ 调试 二选一 Segmented，颜色与 title tag / RuntimeBar 设置面板完全一致。
          - 测试（默认，蓝色）：使用用户填写的全量配置 + 容量预检 + 默认日志；
          - 调试（紫色）：自动装填 1 机器人 / 并发 1 / 详细日志 / 单节点分配。
          切换调试 → 测试 不会回滚已填值（保留用户偏好），与原 Switch 行为一致。 */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 12,
          padding: '8px 12px',
          marginBottom: 12,
          background: debugMode ? 'color-mix(in srgb, var(--color-purple) 8%, transparent)' : 'color-mix(in srgb, var(--color-blue) 6%, transparent)',
          border: `1px solid ${debugMode ? 'color-mix(in srgb, var(--color-purple) 45%, transparent)' : 'color-mix(in srgb, var(--color-blue) 30%, transparent)'}`,
          borderRadius: 6,
        }}
      >
        <Space size={8} style={{ flex: 1, minWidth: 0 }}>
          {debugMode ? (
            <BugOutlined style={{ color: 'var(--color-purple)' }} />
          ) : (
            <CheckCircleOutlined style={{ color: 'var(--color-blue)' }} />
          )}
          <span style={{ fontWeight: 500 }}>{debugMode ? '调试模式' : '测试模式'}</span>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {debugMode
              ? '自动装填 1 个机器人 / 并发 1 / 详细日志 / 单节点分配'
              : '使用你填写的完整配置，启用容量预检与默认日志级别'}
          </Typography.Text>
        </Space>
        <Tooltip title="切换运行模式（不会回滚已填数值）">
          <Segmented
            size="small"
            value={debugMode ? 'debug' : 'test'}
            onChange={(v) => onToggleDebug(v === 'debug')}
            options={[
              {
                label: (
                  <span style={{ color: !debugMode ? 'var(--color-blue)' : undefined, fontWeight: !debugMode ? 600 : undefined }}>
                    <CheckCircleOutlined style={{ marginRight: 4 }} />
                    测试
                  </span>
                ),
                value: 'test',
              },
              {
                label: (
                  <span style={{ color: debugMode ? 'var(--color-purple)' : undefined, fontWeight: debugMode ? 600 : undefined }}>
                    <BugOutlined style={{ marginRight: 4 }} />
                    调试
                  </span>
                ),
                value: 'debug',
              },
            ]}
          />
        </Tooltip>
      </div>

      <Form layout="vertical">
        <Form.Item label="任务名" required>
          <Input value={taskName} onChange={(e) => setTaskName(e.target.value)} placeholder="例：200v200 v1.2" />
        </Form.Item>
        {!rampUpEnabled && (
          <Form.Item
            label="集群总机器人数"
            required
            extra={
              debugMode ? (
                <span>
                  <Tag color="purple" style={{ marginRight: 6 }}>
                    调试
                  </Tag>
                  建议保持 1；集群总容量 {totalCapacity}
                </span>
              ) : (
                `集群总容量 ${totalCapacity}`
              )
            }
          >
            <InputNumber
              min={1}
              max={totalCapacity}
              value={totalBots}
              onChange={(v) => setTotalBots(typeof v === 'number' ? v : 0)}
              style={{ width: '100%' }}
            />
          </Form.Item>
        )}
        {rampUpEnabled && (
          <Form.Item
            label="集群总机器人数"
            extra={
              <span>
                集群总容量 {totalCapacity}
                {rampUpStages.some((s) => s.reset) && (
                  <Typography.Text type="secondary" style={{ marginLeft: 8 }}>
                    · 峰值并发 {peakBots}
                  </Typography.Text>
                )}
              </span>
            }
          >
            <Typography.Text strong>{rampUpSum}</Typography.Text>
            <Typography.Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>
              由各阶段机器人数自动累加
            </Typography.Text>
          </Form.Item>
        )}
        <Form.Item label="并发（每秒新建机器人数）">
          <InputNumber
            min={1}
            max={1000}
            value={robotConfig.concurrency}
            onChange={(v) => setRobotConfig({ concurrency: typeof v === 'number' ? v : 1 })}
            style={{ width: '100%' }}
          />
        </Form.Item>
        <Form.Item
          label={
            <Space size={6}>
              <span>渐进式加压</span>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                分阶段创建机器人，观察逐步增加负载时服务器性能变化
              </Typography.Text>
            </Space>
          }
        >
          <Space direction="vertical" style={{ width: '100%' }} size={8}>
            <Space size={8}>
              <Switch
                checked={rampUpEnabled}
                onChange={(checked) => {
                  setRampUpEnabled(checked);
                  if (checked && rampUpStages.length === 0) {
                    setRampUpStages([{ count: 0, holdSec: 30 }]);
                  }
                }}
                disabled={debugMode}
              />
              {debugMode && (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  调试模式不支持渐进式加压
                </Typography.Text>
              )}
            </Space>
            {rampUpEnabled && !debugMode && (
              <>
                <Table<RampUpStage & { _idx: number }>
                  size="small"
                  pagination={false}
                  dataSource={rampUpStages.map((s, i) => ({ ...s, _idx: i }))}
                  rowKey="_idx"
                  columns={[
                    {
                      title: '阶段',
                      width: 48,
                      render: (_, __, i) => `#${i + 1}`,
                    },
                    {
                      title: '新增机器人数',
                      dataIndex: 'count',
                      width: 120,
                      render: (v: number, _, i) => (
                        <InputNumber
                          size="small"
                          min={1}
                          max={totalCapacity}
                          value={v || undefined}
                          placeholder="数量"
                          onChange={(n) => {
                            const next = [...rampUpStages];
                            next[i] = { ...next[i], count: typeof n === 'number' ? n : 0 };
                            setRampUpStages(next);
                          }}
                          style={{ width: '100%' }}
                        />
                      ),
                    },
                    {
                      title: '并发覆盖',
                      dataIndex: 'concurrency',
                      width: 100,
                      render: (v: number | undefined, _, i) => (
                        <InputNumber
                          size="small"
                          min={0}
                          max={1000}
                          value={v}
                          placeholder="默认"
                          onChange={(n) => {
                            const next = [...rampUpStages];
                            next[i] = { ...next[i], concurrency: typeof n === 'number' ? n : undefined };
                            setRampUpStages(next);
                          }}
                          style={{ width: '100%' }}
                        />
                      ),
                    },
                    {
                      title: '间隔秒数',
                      dataIndex: 'holdSec',
                      width: 100,
                      render: (v: number | undefined, _, i) => (
                        <InputNumber
                          size="small"
                          min={30}
                          max={3600}
                          value={v}
                          placeholder="30"
                          onChange={(n) => {
                            const next = [...rampUpStages];
                            next[i] = { ...next[i], holdSec: typeof n === 'number' ? n : undefined };
                            setRampUpStages(next);
                          }}
                          style={{ width: '100%' }}
                        />
                      ),
                    },
                    {
                      title: '阶段重置',
                      dataIndex: 'reset',
                      width: 80,
                      align: 'center',
                      render: (v: boolean | undefined, _, i) => (
                        <Tooltip title={i === 0 ? '第一阶段无需重置' : '开始前清空已有机器人'}>
                          <Switch
                            size="small"
                            disabled={i === 0}
                            checked={v ?? false}
                            onChange={(checked) => {
                              const next = [...rampUpStages];
                              next[i] = { ...next[i], reset: checked || undefined };
                              setRampUpStages(next);
                            }}
                          />
                        </Tooltip>
                      ),
                    },
                    {
                      title: '',
                      width: 36,
                      render: (_, __, i) =>
                        rampUpStages.length > 1 ? (
                          <Button
                            type="text"
                            size="small"
                            danger
                            icon={<DeleteOutlined />}
                            onClick={() => {
                              setRampUpStages(rampUpStages.filter((_, idx) => idx !== i));
                            }}
                          />
                        ) : null,
                    },
                  ]}
                />
                <Space size={8} style={{ width: '100%', justifyContent: 'space-between' }}>
                  <Button
                    size="small"
                    type="dashed"
                    icon={<PlusOutlined />}
                    onClick={() => setRampUpStages([...rampUpStages, { count: 0, holdSec: 30 }])}
                  >
                    添加阶段
                  </Button>
                  <Typography.Text
                    type={rampUpSum > 0 ? 'success' : 'warning'}
                    style={{ fontSize: 12 }}
                  >
                    合计 {rampUpSum} 台机器人
                    {rampUpSum === 0 && '（请填写各阶段机器人数）'}
                  </Typography.Text>
                </Space>
              </>
            )}
          </Space>
        </Form.Item>
      </Form>

      <Collapse
        size="small"
        style={{ marginTop: 8 }}
        activeKey={advancedExpanded ? ['advanced'] : []}
        onChange={(keys) => {
          const arr = Array.isArray(keys) ? keys : [keys];
          setAdvancedExpanded(arr.includes('advanced'));
        }}
        items={[
          {
            key: 'advanced',
            label: (
              <Space size={6}>
                <span>高级设置</span>
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  State 扩展字段 / 主服务 / 超时 / 日志 / 自动停止
                </Typography.Text>
              </Space>
            ),
            children: (
              <Form layout="vertical">
                <Form.Item
                  label="State 扩展字段"
                  extra="脚本中可通过 robot.get(key) 读取；不配置则返回空值。常用 version/channel/platform"
                >
                  <AuthExtraEditor
                    value={robotConfig.stateExtra}
                    onChange={(v) => setRobotConfig({ stateExtra: v })}
                  />
                </Form.Item>
                <Form.Item label="账号前缀" extra="如 bot_/qa_，默认 bot_">
                  <Input
                    value={robotConfig.accountPrefix ?? ''}
                    onChange={(e) => setRobotConfig({ accountPrefix: e.target.value })}
                    placeholder="bot_"
                  />
                </Form.Item>
                <Form.Item
                  label="账号编号起点"
                  extra={`默认 0；账号格式 ${robotConfig.accountPrefix || 'bot_'}<startNumber + N>。已有 bot_0~bot_99 在线时可设 100 避免撞车。`}
                >
                  <InputNumber
                    min={0}
                    max={1_000_000}
                    value={robotConfig.startNumber ?? 0}
                    onChange={(v) => setRobotConfig({ startNumber: typeof v === 'number' ? v : 0 })}
                    style={{ width: '100%' }}
                  />
                </Form.Item>
                <Form.Item label="主连接服务名" extra="主连接对应的服务标识，默认 logic">
                  <Input
                    value={robotConfig.mainService ?? ''}
                    onChange={(e) => setRobotConfig({ mainService: e.target.value })}
                    placeholder="logic"
                  />
                </Form.Item>
                <Form.Item label="TCP 超时（秒）">
                  <InputNumber
                    min={1}
                    max={600}
                    value={robotConfig.timeoutSec}
                    onChange={(v) => setRobotConfig({ timeoutSec: typeof v === 'number' ? v : 60 })}
                    style={{ width: '100%' }}
                  />
                </Form.Item>
                <Form.Item label="HTTP 超时（秒）">
                  <InputNumber
                    min={1}
                    max={600}
                    value={robotConfig.httpTimeoutSec ?? 10}
                    onChange={(v) => setRobotConfig({ httpTimeoutSec: typeof v === 'number' ? v : 10 })}
                    style={{ width: '100%' }}
                  />
                </Form.Item>
                <Form.Item label="心跳间隔（秒）">
                  <InputNumber
                    min={1}
                    max={300}
                    value={robotConfig.heartbeatSec ?? 5}
                    onChange={(v) => setRobotConfig({ heartbeatSec: typeof v === 'number' ? v : 5 })}
                    style={{ width: '100%' }}
                  />
                </Form.Item>
                <Form.Item
                  label="Apdex 满意阈值（毫秒）"
                  extra="RTT < T 计为满意；T ≤ RTT < 4T 计为容忍；RTT ≥ 4T 计为不满意。默认 100ms"
                >
                  <InputNumber
                    min={1}
                    max={10000}
                    value={robotConfig.apdexT ?? 100}
                    onChange={(v) => setRobotConfig({ apdexT: typeof v === 'number' ? v : 100 })}
                    style={{ width: '100%' }}
                  />
                </Form.Item>
                <Form.Item
                  label={
                    <Space size={6}>
                      <span>节点日志等级</span>
                      {robotConfig.logLevel === 'debug' && (
                        <Tag color="purple" style={{ margin: 0 }}>
                          debug
                        </Tag>
                      )}
                    </Space>
                  }
                  extra={
                    robotConfig.logLevel === 'debug'
                      ? '将打印全部收发包与字段绑定，量大；任务结束后自动恢复原等级。'
                      : '任务期临时切换节点进程日志等级，结束后自动恢复。'
                  }
                >
                  <Select<LogLevel>
                    value={robotConfig.logLevel ?? 'info'}
                    onChange={(v) => setRobotConfig({ logLevel: v })}
                    options={LOG_LEVEL_OPTIONS.map((o) => ({
                      value: o.value,
                      label: (
                        <Space size={6}>
                          <span style={{ fontFamily: 'var(--font-mono, monospace)' }}>{o.label}</span>
                          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                            {o.desc}
                          </Typography.Text>
                        </Space>
                      ),
                    }))}
                    style={{ width: '100%' }}
                  />
                </Form.Item>
                <Form.Item label="自动停止时间（可选）">
                  <DatePicker
                    showTime
                    value={deadline ? dayjs(deadline) : null}
                    onChange={(d: Dayjs | null) => setDeadline(d ? d.toISOString() : null)}
                    style={{ width: '100%' }}
                  />
                </Form.Item>
              </Form>
            ),
          },
        ]}
      />

      <Descriptions size="small" column={2} style={{ marginTop: 8 }}>
        <Descriptions.Item label="Proto 文件">
          {protos.length === 0 ? <Tag color="default">无</Tag> : <Tag color="blue">{protos.length} 个</Tag>}
        </Descriptions.Item>
        <Descriptions.Item label="Lua 脚本">
          <Tooltip
            title={
              `flow 引用 ${refScriptCount} 个；本地共 ${scripts.length} 个（含历史）；` +
              `缺失 ${missingScripts.length} 个`
            }
          >
            <Space size={4} wrap>
              {syncing && <Tag color="processing">同步中…</Tag>}
              <Tag color="purple">引用 {refScriptCount}</Tag>
              <Tag color="blue">本地 {scripts.length}</Tag>
              {missingScripts.length > 0 && (
                <Tag color="red">缺失 {missingScripts.length}</Tag>
              )}
            </Space>
          </Tooltip>
        </Descriptions.Item>
      </Descriptions>

      {protos.length === 0 && (
        <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: 8 }}>
          未上传的协议文件由服务器提供默认值；如需自定义请到「资源管理」上传。
        </Typography.Text>
      )}

      {missingScripts.length > 0 && (
        <Alert
          type="error"
          showIcon
          style={{ marginTop: 12 }}
          message={`缺少 ${missingScripts.length} 个 lua 脚本，启动会失败`}
          description={
            <>
              <div style={{ marginBottom: 4 }}>{missingScripts.join(', ')}</div>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                请到「资源管理」上传，或在动作里直接编辑（编辑器写完会自动入库）。
              </Typography.Text>
            </>
          }
        />
      )}

      {capacityWarn && (
        <Alert
          type="warning"
          showIcon
          style={{ marginTop: 12 }}
          message={`集群总容量 ${totalCapacity}，本次申请 ${totalBots}`}
          description="请减少机器人数，或增加压测节点。"
        />
      )}

      {noAgentBlock && (
        <Alert
          type="error"
          showIcon
          icon={<ThunderboltOutlined />}
          style={{ marginTop: 12 }}
          message="没有在线的节点"
          description="请确认至少有一台节点程序已经成功注册。"
        />
      )}
      </Modal>

      <Modal
        title="本次任务资源存在冲突"
        open={diffChoiceOpen}
        onCancel={() => setDiffChoiceOpen(false)}
        footer={
          <Space>
            <Button onClick={() => setDiffChoiceOpen(false)}>取消</Button>
            <Button
              onClick={() => {
                setDiffChoiceOpen(false);
                setDiffResolveOpen(true);
              }}
            >
              处理冲突
            </Button>
            <Button type="primary" loading={submitting} onClick={handleOverwriteRun}>
              用本地存储覆盖并运行
            </Button>
          </Space>
        }
        width={520}
        destroyOnHidden
        styles={{ mask: { zIndex: popupZ + 10 }, wrapper: { zIndex: popupZ + 11 } }}
      >
        <Typography.Paragraph type="secondary">
          以下资源在本地存储和服务器中都发生了变化。你可以逐项处理冲突，或使用本地存储中的版本覆盖服务器并运行。
        </Typography.Paragraph>
        {taskDiffResult && (
          <Space size={8} wrap>
            {taskDiffResult.conflicts.length > 0 && <Tag color="orange">内容不同 {taskDiffResult.conflicts.length}</Tag>}
            {taskDiffResult.removed.length > 0 && <Tag color="red">服务器未找到 {taskDiffResult.removed.length}</Tag>}
          </Space>
        )}
      </Modal>

      {taskDiffResult && (
        <BaselineSyncModal
          open={diffResolveOpen}
          result={taskDiffResult}
          title="处理本次任务资源冲突"
          description="请确认本次任务使用本地存储版本还是服务器版本。应用后将继续启动任务。"
          localLabel="使用本地存储版本"
          serverLabel="使用服务器版本"
          onClose={() => setDiffResolveOpen(false)}
          onResolved={handleDiffResolved}
        />
      )}
    </>
  );
}
