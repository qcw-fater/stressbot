package admin

// MySQL DDL — 历史归档 5+1 张表。
// Admin 启动时通过 HistoryStore.InitSchema() 自动执行（仅创建不存在的表）。

const ddlTaskHistory = `
CREATE TABLE IF NOT EXISTS task_history (
    id              VARCHAR(32)  NOT NULL PRIMARY KEY,
    name            VARCHAR(255) NOT NULL DEFAULT '',
    state           VARCHAR(32)  NOT NULL DEFAULT '',
    total_bots      INT          NOT NULL DEFAULT 0,
    agent_count     INT          NOT NULL DEFAULT 0,
    created_at      DATETIME(3)  NOT NULL,
    started_at      DATETIME(3)  NULL,
    stopped_at      DATETIME(3)  NULL,
    duration_sec    INT          NOT NULL DEFAULT 0,
    error_msg       TEXT,
    starred         TINYINT(1)   NOT NULL DEFAULT 0,
    tags            JSON         NULL,
    note            TEXT,
    config_summary  JSON         NULL,
    INDEX idx_state (state),
    INDEX idx_created (created_at DESC),
    INDEX idx_starred (starred)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

const ddlTaskAssignment = `
CREATE TABLE IF NOT EXISTS task_assignment (
    id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    task_id         VARCHAR(32)  NOT NULL,
    agent_id        VARCHAR(64)  NOT NULL,
    start_number    INT          NOT NULL DEFAULT 0,
    total_bots      INT          NOT NULL DEFAULT 0,
    INDEX idx_task (task_id),
    FOREIGN KEY (task_id) REFERENCES task_history(id) ON DELETE CASCADE
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
    INDEX idx_task (task_id),
    FOREIGN KEY (task_id) REFERENCES task_history(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

const ddlTaskAggregated = `
CREATE TABLE IF NOT EXISTS task_aggregated (
    task_id         VARCHAR(32)  NOT NULL PRIMARY KEY,
    final_stress    JSON         NULL,
    final_system    JSON         NULL,
    FOREIGN KEY (task_id) REFERENCES task_history(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

const ddlTaskTimeseries = `
CREATE TABLE IF NOT EXISTS task_timeseries (
    id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    task_id         VARCHAR(32)  NOT NULL,
    sampled_at      DATETIME(3)  NOT NULL,
    elapsed_sec     INT          NOT NULL DEFAULT 0,
    data_type       VARCHAR(32)  NOT NULL,
    snapshot        JSON         NOT NULL,
    INDEX idx_task_type (task_id, data_type, elapsed_sec),
    FOREIGN KEY (task_id) REFERENCES task_history(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

const ddlTaskConfigArchive = `
CREATE TABLE IF NOT EXISTS task_config_archive (
    task_id         VARCHAR(32)  NOT NULL PRIMARY KEY,
    flow_json       MEDIUMBLOB   NULL,
    proto_files     MEDIUMBLOB   NULL,
    lua_scripts     MEDIUMBLOB   NULL,
    robot_config    JSON         NULL,
    FOREIGN KEY (task_id) REFERENCES task_history(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

var allDDL = []string{
	ddlTaskHistory,
	ddlTaskAssignment,
	ddlTaskReport,
	ddlTaskAggregated,
	ddlTaskTimeseries,
	ddlTaskConfigArchive,
}
