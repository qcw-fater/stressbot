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
 *     启动时 skipCapacityCheck=true（容量不足不再阻塞，让服务端兜底）；
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
  Tag,
  Tooltip,
  Typography,
} from 'antd';
import type { LogLevel } from '@/types/api';
import { AuthExtraEditor } from './AuthExtraEditor';
import { BugOutlined, CheckCircleOutlined, ThunderboltOutlined } from '@ant-design/icons';
import { useEffect, useMemo, useRef, useState } from 'react';
import dayjs, { type Dayjs } from 'dayjs';
import { useShallow } from 'zustand/react/shallow';
import { showApiError, startTask, useRuntimeStore } from '@/services';
import { listProto, listScript, type ResourceFile } from '@/services/resourcesStore';
import { syncFlowScriptsToIdb, collectFlowScriptNames } from '@/services/scriptSync';
import { useFlowStore } from '@/components/FlowEditor/store/flowStore';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';

export interface TaskStartModalProps {
  open: boolean;
  onClose: () => void;
  /** 启动成功后的回调（拿到 taskId） */
  onStarted?: (taskId: string) => void;
}

const DEBUG_TASK_NAME_PLACEHOLDERS = new Set(['', '未命名任务']);

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
  const {
    taskName,
    totalBots,
    robotConfig,
    deadline,
    agents,
    setTaskName,
    setTotalBots,
    setRobotConfig,
    setDeadline,
  } = useRuntimeStore(
    useShallow((s) => ({
      taskName: s.taskName,
      totalBots: s.totalBots,
      robotConfig: s.robotConfig,
      deadline: s.deadline,
      agents: s.agents,
      setTaskName: s.setTaskName,
      setTotalBots: s.setTotalBots,
      setRobotConfig: s.setRobotConfig,
      setDeadline: s.setDeadline,
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

  // 弹窗打开 → 先把 flow 引用、IDB 缺失的脚本从默认基线拉回 IDB（保护已编辑稿不覆盖），
  // 再 listProto / listScript 取最终 IDB 全集，让"Lua 脚本"行展示的数字与 startTask
  // 实际会上传的 multipart 内容完全一致，避免用户看到"6 个"实际上传却是"7 个"的错觉。
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setSyncing(true);
    (async () => {
      try {
        const flow = useFlowStore.getState().toTaskFlow();
        const refNames = collectFlowScriptNames(flow);
        const sync = await syncFlowScriptsToIdb(flow);
        const [p, s] = await Promise.all([listProto(), listScript()]);
        if (cancelled) return;
        setProtos(p);
        setScripts(s);
        setRefScriptCount(refNames.length);
        setMissingScripts(sync.missing);
      } finally {
        if (!cancelled) setSyncing(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [open]);

  // 弹窗打开瞬间若已经处于调试模式，主动装填一次（用户上次留下的值可能是 100/50）。
  // 用 ref 防止依赖项波动重复装填。
  const filledRef = useRef(false);
  useEffect(() => {
    if (!open) {
      filledRef.current = false;
      return;
    }
    if (debugMode && !filledRef.current) {
      applyDebugPreset();
      filledRef.current = true;
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, debugMode]);

  const availableBots = useMemo(() => {
    return (agents ?? [])
      .filter((a) => a.status === 'idle' || a.status === 'busy')
      .reduce((sum, a) => sum + Math.max(0, a.maxBots - a.currentBots), 0);
  }, [agents]);

  const onlineAgents = (agents ?? []).filter((a) => a.status !== 'offline').length;

  // 调试模式下不再硬性禁止超容量；普通模式按容量预检。
  const capacityWarn = !debugMode && totalBots > availableBots;
  const noAgentBlock = onlineAgents === 0; // 无 Agent 在线连调试也跑不起来，仍禁用启动

  // authAddr 必须带 scheme，否则后端 net/http 会报 "unsupported protocol scheme"，
  // 被 lua 兜底变成 -1，业务脚本看到的就是"错误码 1"，根因被吞掉。
  const authAddrTrim = robotConfig.authAddr.trim();
  const authAddrInvalid =
    authAddrTrim !== '' &&
    !authAddrTrim.startsWith('http://') &&
    !authAddrTrim.startsWith('https://');

  function applyDebugPreset() {
    setTotalBots(DEBUG_PRESET.totalBots);
    setRobotConfig({
      concurrency: DEBUG_PRESET.concurrency,
      logLevel: DEBUG_PRESET.logLevel,
    });
    if (DEBUG_TASK_NAME_PLACEHOLDERS.has(taskName.trim())) {
      setTaskName(`debug · ${dayjs().format('MMDD-HHmm')}`);
    }
  }

  function onToggleDebug(v: boolean) {
    setDebugMode(v);
    if (v) {
      applyDebugPreset();
      filledRef.current = true;
    }
  }

  const handleSubmit = async () => {
    setSubmitting(true);
    try {
      const id = await startTask({
        name: taskName,
        totalBots,
        robotConfig,
        deadline: deadline ?? undefined,
        skipCapacityCheck: debugMode,
      });
      onStarted?.(id);
      onClose();
    } catch (e) {
      showApiError(e);
    } finally {
      setSubmitting(false);
    }
  };

  return (
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
          authAddrInvalid ||
          !authAddrTrim ||
          !taskName.trim() ||
          totalBots <= 0,
        danger: debugMode ? false : undefined,
      }}
      width={620}
      destroyOnHidden
    >
      {/* 模式选择条：测试 ↔ 调试 二选一 Segmented，颜色与 title tag / RuntimeBar 设置面板完全一致。
          - 测试（默认，蓝色）：使用用户填写的全量配置 + 容量预检 + 默认日志；
          - 调试（紫色）：自动装填 1 机器人 / 并发 1 / 跳过容量预检 / 日志=debug。
          切换调试 → 测试 不会回滚已填值（保留用户偏好），与原 Switch 行为一致。 */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 12,
          padding: '8px 12px',
          marginBottom: 12,
          background: debugMode ? 'rgba(146, 84, 222, 0.08)' : 'rgba(22, 119, 255, 0.06)',
          border: `1px solid ${debugMode ? 'rgba(146,84,222,0.45)' : 'rgba(22,119,255,0.30)'}`,
          borderRadius: 6,
        }}
      >
        <Space size={8} style={{ flex: 1, minWidth: 0 }}>
          {debugMode ? (
            <BugOutlined style={{ color: '#9254de' }} />
          ) : (
            <CheckCircleOutlined style={{ color: '#1677ff' }} />
          )}
          <span style={{ fontWeight: 500 }}>{debugMode ? '调试模式' : '测试模式'}</span>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {debugMode
              ? '一键装填 1 个机器人 / 并发 1 / 日志 debug / 跳过容量预检'
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
        </Tooltip>
      </div>

      <Form layout="vertical">
        <Form.Item label="任务名" required>
          <Input value={taskName} onChange={(e) => setTaskName(e.target.value)} placeholder="例：200v200 v1.2" />
        </Form.Item>
        <Form.Item
          label="集群总机器人数"
          required
          extra={
            debugMode ? (
              <span>
                <Tag color="purple" style={{ marginRight: 6 }}>
                  调试
                </Tag>
                建议保持 1；当前剩余容量约 {availableBots}
              </span>
            ) : (
              `集群剩余容量约 ${availableBots}`
            )
          }
        >
          <InputNumber
            min={1}
            max={100000}
            value={totalBots}
            onChange={(v) => setTotalBots(typeof v === 'number' ? v : 0)}
            style={{ width: '100%' }}
          />
        </Form.Item>
        <Form.Item
          label="Auth 地址"
          required
          validateStatus={authAddrInvalid ? 'error' : undefined}
          help={
            authAddrInvalid
              ? '必须以 http:// 或 https:// 开头，否则脚本调用 http_post 会失败（错误码 1）'
              : '与单机 conf/config.json 中 auth.address 一致；启动时会下发到 Agent'
          }
        >
          <Input
            value={robotConfig.authAddr}
            onChange={(e) => setRobotConfig({ authAddr: e.target.value })}
            placeholder="例：http://auth.example.com:20000"
            status={authAddrInvalid ? 'error' : undefined}
          />
        </Form.Item>
        <Form.Item label="并发（每秒新建机器人数）">
          <InputNumber
            min={1}
            max={1000}
            value={robotConfig.concurrency}
            onChange={(v) => setRobotConfig({ concurrency: typeof v === 'number' ? v : 1 })}
            style={{ width: '100%' }}
          />
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
                  Auth 扩展字段 / 主服务 / 超时 / 日志 / 自动停止
                </Typography.Text>
              </Space>
            ),
            children: (
              <Form layout="vertical">
                <Form.Item
                  label="Auth 扩展字段（authExtra）"
                  extra="lua 脚本通过 robot.get(key) 读取；不配置则取 nil。常用 version/channel/platform"
                >
                  <AuthExtraEditor
                    value={robotConfig.authExtra}
                    onChange={(v) => setRobotConfig({ authExtra: v })}
                  />
                </Form.Item>
                <Form.Item label="账号前缀（accountPrefix）" extra="如 bot_/qa_，默认 bot_">
                  <Input
                    value={robotConfig.accountPrefix ?? ''}
                    onChange={(e) => setRobotConfig({ accountPrefix: e.target.value })}
                    placeholder="bot_"
                  />
                </Form.Item>
                <Form.Item
                  label="账号编号起点（startNumber）"
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
                <Form.Item label="主连接服务名（mainService）" extra="主连接对应的服务标识，默认 logic">
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
                  extra="动作响应时间 ≤ T 计为完全满意；> 4T 计为不满意。默认 100ms"
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
                      <span>Agent 日志等级</span>
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
                      : '任务期临时切换 Agent 进程日志等级，结束后自动恢复。'
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

      <Descriptions size="small" column={2} bordered style={{ marginTop: 8 }}>
        <Descriptions.Item label="Proto 文件">
          {protos.length === 0 ? <Tag color="default">无</Tag> : <Tag color="blue">{protos.length} 个</Tag>}
        </Descriptions.Item>
        <Descriptions.Item label="Lua 脚本">
          <Tooltip
            title={
              `flow 引用 ${refScriptCount} 个；本地 IDB 共 ${scripts.length} 个（含历史）；` +
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
          未上传的 proto 由 Admin 兜底默认值；如需自定义请到「资源管理」上传。
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
          message={`集群剩余容量 ${availableBots}，本次申请 ${totalBots}`}
          description="请减少机器人数，或先停止其他任务释放节点；或开启「调试模式」跳过容量预检。"
        />
      )}

      {noAgentBlock && (
        <Alert
          type="error"
          showIcon
          icon={<ThunderboltOutlined />}
          style={{ marginTop: 12 }}
          message="没有在线的 Agent"
          description="请确认至少有一台 stressbot-agent 已经成功注册到 Admin。"
        />
      )}
    </Modal>
  );
}
