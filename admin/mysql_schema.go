package admin

// MySQL DDL — Admin 所有表（历史归档 + 未来流程模板库）。
// Admin 启动时通过 HistoryStore.initSchema() 自动执行（仅创建不存在的表）。
// 已有数据库升级见 deploy/upgrade.sql（INFORMATION_SCHEMA 守卫、幂等）。
// 收藏/标签/备注统一存于 task_meta（stage_index=-1 为任务级），不再是 task_history 的列。

const ddlTaskHistory = `
CREATE TABLE IF NOT EXISTS task_history (
    id              VARCHAR(32)  NOT NULL PRIMARY KEY,
    name            VARCHAR(255) NOT NULL DEFAULT '',
    state           VARCHAR(32)  NOT NULL DEFAULT '',
    total_bots      INT          NOT NULL DEFAULT 0,
    agent_count     INT          NOT NULL DEFAULT 0,
    active_agent_count INT       NOT NULL DEFAULT 0,
    created_at      DATETIME(3)  NOT NULL,
    started_at      DATETIME(3)  NULL,
    stopped_at      DATETIME(3)  NULL,
    duration_sec    INT          NOT NULL DEFAULT 0,
    error_msg       TEXT,
    debug_mode      TINYINT(1)   NOT NULL DEFAULT 0,
    config_summary  JSON         NULL,
    stage_count     INT          NOT NULL DEFAULT 0,
    flow_template_id VARCHAR(32) NULL, -- 来源流程模板 ID（逻辑外键，可空；模板删除不影响历史快照）
    INDEX idx_state (state),
    INDEX idx_created (created_at DESC),
    INDEX idx_started (started_at),
    INDEX idx_stopped (stopped_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

const ddlTaskAssignment = `
CREATE TABLE IF NOT EXISTS task_assignment (
    id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    task_id         VARCHAR(32)  NOT NULL,
    agent_id        VARCHAR(64)  NOT NULL,
    start_number    INT          NOT NULL DEFAULT 0,
    total_bots      INT          NOT NULL DEFAULT 0,
    INDEX idx_task (task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

const ddlTaskReport = `
CREATE TABLE IF NOT EXISTS task_report (
    id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    task_id         VARCHAR(32)  NOT NULL,
    agent_id        VARCHAR(64)  NOT NULL,
    agent_name      VARCHAR(255) NOT NULL DEFAULT '',
    result          VARCHAR(32)  NOT NULL DEFAULT '',
    error_msg       TEXT,
    finished_at     DATETIME(3)  NULL,
    final_snapshot  JSON         NULL,
    cleanup_status  JSON         NULL,
    stage_index     INT          NOT NULL DEFAULT -1,
    INDEX idx_task (task_id),
    INDEX idx_task_stage (task_id, stage_index)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

const ddlTaskAggregated = `
CREATE TABLE IF NOT EXISTS task_aggregated (
    task_id         VARCHAR(32)  NOT NULL,
    stage_index     INT          NOT NULL DEFAULT -1,
    final_stress    JSON         NULL,
    final_system    JSON         NULL,
    PRIMARY KEY (task_id, stage_index)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

const ddlTaskTimeseries = `
CREATE TABLE IF NOT EXISTS task_timeseries (
    id                  BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    task_id             VARCHAR(32)  NOT NULL,
    sampled_at          DATETIME(3)  NOT NULL,
    elapsed_sec         INT          NOT NULL DEFAULT 0,
    stage_index         INT          NOT NULL DEFAULT -1,
    total_qps           DOUBLE       NOT NULL DEFAULT 0,
    rtt_apdex           DOUBLE       NOT NULL DEFAULT 0,
    total_duration_apdex DOUBLE      NOT NULL DEFAULT 0,
    rtt_avg_ms          DOUBLE       NOT NULL DEFAULT 0,
    rtt_p95_ms          DOUBLE       NOT NULL DEFAULT 0,
    rtt_p99_ms          DOUBLE       NOT NULL DEFAULT 0,
    total_duration_avg_ms DOUBLE     NOT NULL DEFAULT 0,
    total_duration_p95_ms DOUBLE     NOT NULL DEFAULT 0,
    total_duration_p99_ms DOUBLE     NOT NULL DEFAULT 0,
    client_avg_ms       DOUBLE       NOT NULL DEFAULT 0,
    encode_avg_ms       DOUBLE       NOT NULL DEFAULT 0,
    decode_avg_ms       DOUBLE       NOT NULL DEFAULT 0,
    bots_running        INT          NOT NULL DEFAULT 0,
    bots_errored        INT          NOT NULL DEFAULT 0,
    send_kbps           DOUBLE       NOT NULL DEFAULT 0,
    recv_kbps           DOUBLE       NOT NULL DEFAULT 0,
    avg_cpu_percent     DOUBLE       NOT NULL DEFAULT 0,
    max_cpu_percent     DOUBLE       NOT NULL DEFAULT 0,
    avg_mem_percent     DOUBLE       NOT NULL DEFAULT 0,
    max_mem_percent     DOUBLE       NOT NULL DEFAULT 0,
    goroutines          INT          NOT NULL DEFAULT 0,
    threads             INT          NOT NULL DEFAULT 0,
    fds                 INT          NOT NULL DEFAULT 0,
    online_count        INT          NOT NULL DEFAULT 0,
    offline_count       INT          NOT NULL DEFAULT 0,
    INDEX idx_task_elapsed (task_id, elapsed_sec),
    INDEX idx_task_stage_elapsed (task_id, stage_index, elapsed_sec)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

const ddlTaskConfigArchive = `
CREATE TABLE IF NOT EXISTS task_config_archive (
    task_id         VARCHAR(32)  NOT NULL PRIMARY KEY,
    flow_json       MEDIUMBLOB   NULL,
    proto_files     MEDIUMBLOB   NULL,
    lua_scripts     MEDIUMBLOB   NULL,
    robot_config    JSON         NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

// task_meta — 任务/阶段段落级元数据（收藏/标签/备注），统一按 (task_id, stage_index) 键，
// 与 task_report / task_aggregated / task_timeseries 同构：stage_index=-1 为整体（任务级，
// 所有任务都用），stage_index>=1 为 reset 渐进式加压的各阶段段落（各自独立一份）。
// 行按需懒创建：未编辑过的（任务或段落）无行，读取时取默认值（未收藏 / 无标签 / 空备注）。
const ddlTaskMeta = `
CREATE TABLE IF NOT EXISTS task_meta (
    task_id         VARCHAR(32)  NOT NULL,
    stage_index     INT          NOT NULL DEFAULT -1,
    starred         TINYINT(1)   NOT NULL DEFAULT 0,
    tags            JSON         NULL,
    note            TEXT,
    updated_at      DATETIME(3)  NOT NULL,
    PRIMARY KEY (task_id, stage_index),
    INDEX idx_starred (starred)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

const ddlTaskAgentEvents = `
CREATE TABLE IF NOT EXISTS task_agent_events (
    id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    task_id         VARCHAR(32)  NOT NULL,
    agent_id        VARCHAR(64)  NOT NULL,
    agent_name      VARCHAR(255) NOT NULL DEFAULT '',
    event_type      VARCHAR(32)  NOT NULL,
    timestamp       DATETIME(3)  NOT NULL,
    detail          TEXT,
    INDEX idx_task (task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

// flow_template — 流程模板库。命名流程的 flow + layout，供「选择已保存流程启动」复用。
// 不内嵌 Lua/proto/adapter：资源走独立管理链路，启动时按 flow 引用收集。
// node_count/action_count 由服务端保存时从 flow_json 计算（不由前端信任传入）。
const ddlFlowTemplate = `
CREATE TABLE IF NOT EXISTS flow_template (
    id           VARCHAR(32)  NOT NULL PRIMARY KEY,
    name         VARCHAR(80)  NOT NULL,
    flow_json    MEDIUMBLOB   NOT NULL,
    layout_json  MEDIUMBLOB   NULL,
    node_count   INT          NOT NULL DEFAULT 0,
    action_count INT          NOT NULL DEFAULT 0,
    created_at   DATETIME(3)  NOT NULL,
    updated_at   DATETIME(3)  NOT NULL,
    INDEX idx_flow_template_updated (updated_at DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

var allDDL = []string{
	ddlTaskHistory,
	ddlTaskAssignment,
	ddlTaskReport,
	ddlTaskAggregated,
	ddlTaskTimeseries,
	ddlTaskConfigArchive,
	ddlTaskAgentEvents,
	ddlTaskMeta,
	ddlFlowTemplate,
}
