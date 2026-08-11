package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

type columnMigration struct {
	table  string
	column string
	ddl    string
}

func reconcileHistoryMigration() *goose.Migration {
	migration := goose.NewGoMigration(2, &goose.GoFunc{RunDB: reconcileHistory}, nil)
	migration.Source = "00002_reconcile_history.go"
	return migration
}

func reconcileHistory(ctx context.Context, db *sql.DB) error {
	database, err := currentDatabase(ctx, db)
	if err != nil {
		return err
	}

	columns := []columnMigration{
		{"task_history", "active_agent_count", "ALTER TABLE task_history ADD COLUMN active_agent_count INT NOT NULL DEFAULT 0 AFTER agent_count"},
		{"task_history", "debug_mode", "ALTER TABLE task_history ADD COLUMN debug_mode TINYINT(1) NOT NULL DEFAULT 0 AFTER error_msg"},
		{"task_history", "stage_count", "ALTER TABLE task_history ADD COLUMN stage_count INT NOT NULL DEFAULT 0 AFTER config_summary"},
		{"task_history", "flow_template_id", "ALTER TABLE task_history ADD COLUMN flow_template_id VARCHAR(32) NULL AFTER stage_count"},
		{"task_report", "cleanup_status", "ALTER TABLE task_report ADD COLUMN cleanup_status JSON NULL AFTER final_snapshot"},
		{"task_report", "stage_index", "ALTER TABLE task_report ADD COLUMN stage_index INT NOT NULL DEFAULT -1 AFTER cleanup_status"},
		{"task_aggregated", "stage_index", "ALTER TABLE task_aggregated ADD COLUMN stage_index INT NOT NULL DEFAULT -1 AFTER task_id"},
		{"task_config_archive", "codecs", "ALTER TABLE task_config_archive ADD COLUMN codecs MEDIUMBLOB NULL AFTER lua_scripts"},
		{"task_config_archive", "error_map", "ALTER TABLE task_config_archive ADD COLUMN error_map MEDIUMBLOB NULL AFTER codecs"},
		{"task_timeseries", "stage_index", "ALTER TABLE task_timeseries ADD COLUMN stage_index INT NOT NULL DEFAULT -1 AFTER elapsed_sec"},
		{"task_timeseries", "window_from", "ALTER TABLE task_timeseries ADD COLUMN window_from DATETIME(6) NULL AFTER stage_index"},
		{"task_timeseries", "window_to", "ALTER TABLE task_timeseries ADD COLUMN window_to DATETIME(6) NULL AFTER window_from"},
		{"task_timeseries", "history_batch_token", "ALTER TABLE task_timeseries ADD COLUMN history_batch_token BINARY(32) NULL AFTER window_to"},
		{"task_timeseries", "sample_count", "ALTER TABLE task_timeseries ADD COLUMN sample_count BIGINT NULL AFTER history_batch_token"},
		{"task_timeseries", "total_qps", "ALTER TABLE task_timeseries ADD COLUMN total_qps DOUBLE NOT NULL DEFAULT 0 AFTER sample_count"},
		{"task_timeseries", "rtt_apdex", "ALTER TABLE task_timeseries ADD COLUMN rtt_apdex DOUBLE NULL AFTER total_qps"},
		{"task_timeseries", "listen_wait_p99_ms", "ALTER TABLE task_timeseries ADD COLUMN listen_wait_p99_ms DOUBLE NULL AFTER rtt_apdex"},
		{"task_timeseries", "rtt_avg_ms", "ALTER TABLE task_timeseries ADD COLUMN rtt_avg_ms DOUBLE NULL AFTER listen_wait_p99_ms"},
		{"task_timeseries", "rtt_p50_ms", "ALTER TABLE task_timeseries ADD COLUMN rtt_p50_ms DOUBLE NULL AFTER rtt_avg_ms"},
		{"task_timeseries", "rtt_p90_ms", "ALTER TABLE task_timeseries ADD COLUMN rtt_p90_ms DOUBLE NULL AFTER rtt_p50_ms"},
		{"task_timeseries", "rtt_p95_ms", "ALTER TABLE task_timeseries ADD COLUMN rtt_p95_ms DOUBLE NULL AFTER rtt_p90_ms"},
		{"task_timeseries", "rtt_p99_ms", "ALTER TABLE task_timeseries ADD COLUMN rtt_p99_ms DOUBLE NULL AFTER rtt_p95_ms"},
		{"task_timeseries", "active_connections", "ALTER TABLE task_timeseries ADD COLUMN active_connections BIGINT NULL AFTER rtt_p99_ms"},
		{"task_timeseries", "closed_connections", "ALTER TABLE task_timeseries ADD COLUMN closed_connections BIGINT NULL AFTER active_connections"},
		{"task_timeseries", "dropped_connections", "ALTER TABLE task_timeseries ADD COLUMN dropped_connections BIGINT NULL AFTER closed_connections"},
		{"task_timeseries", "net_send_bytes_per_sec", "ALTER TABLE task_timeseries ADD COLUMN net_send_bytes_per_sec DOUBLE NULL AFTER dropped_connections"},
		{"task_timeseries", "net_recv_bytes_per_sec", "ALTER TABLE task_timeseries ADD COLUMN net_recv_bytes_per_sec DOUBLE NULL AFTER net_send_bytes_per_sec"},
		{"task_timeseries", "assigned_agents", "ALTER TABLE task_timeseries ADD COLUMN assigned_agents BIGINT NULL AFTER net_recv_bytes_per_sec"},
		{"task_timeseries", "reporting_agents", "ALTER TABLE task_timeseries ADD COLUMN reporting_agents BIGINT NULL AFTER assigned_agents"},
		{"task_timeseries", "reporting_coverage", "ALTER TABLE task_timeseries ADD COLUMN reporting_coverage DOUBLE NULL AFTER reporting_agents"},
		{"task_timeseries", "total_duration_avg_ms", "ALTER TABLE task_timeseries ADD COLUMN total_duration_avg_ms DOUBLE NULL AFTER reporting_coverage"},
		{"task_timeseries", "total_duration_p95_ms", "ALTER TABLE task_timeseries ADD COLUMN total_duration_p95_ms DOUBLE NULL AFTER total_duration_avg_ms"},
		{"task_timeseries", "total_duration_p99_ms", "ALTER TABLE task_timeseries ADD COLUMN total_duration_p99_ms DOUBLE NULL AFTER total_duration_p95_ms"},
		{"task_timeseries", "client_avg_ms", "ALTER TABLE task_timeseries ADD COLUMN client_avg_ms DOUBLE NULL AFTER total_duration_p99_ms"},
		{"task_timeseries", "encode_avg_ms", "ALTER TABLE task_timeseries ADD COLUMN encode_avg_ms DOUBLE NULL AFTER client_avg_ms"},
		{"task_timeseries", "decode_avg_ms", "ALTER TABLE task_timeseries ADD COLUMN decode_avg_ms DOUBLE NULL AFTER encode_avg_ms"},
		{"task_timeseries", "bots_running", "ALTER TABLE task_timeseries ADD COLUMN bots_running INT NOT NULL DEFAULT 0 AFTER decode_avg_ms"},
		{"task_timeseries", "bots_errored", "ALTER TABLE task_timeseries ADD COLUMN bots_errored INT NOT NULL DEFAULT 0 AFTER bots_running"},
		{"task_timeseries", "send_kbps", "ALTER TABLE task_timeseries ADD COLUMN send_kbps DOUBLE NOT NULL DEFAULT 0 AFTER bots_errored"},
		{"task_timeseries", "recv_kbps", "ALTER TABLE task_timeseries ADD COLUMN recv_kbps DOUBLE NOT NULL DEFAULT 0 AFTER send_kbps"},
		{"task_timeseries", "avg_cpu_percent", "ALTER TABLE task_timeseries ADD COLUMN avg_cpu_percent DOUBLE NOT NULL DEFAULT 0 AFTER recv_kbps"},
		{"task_timeseries", "max_cpu_percent", "ALTER TABLE task_timeseries ADD COLUMN max_cpu_percent DOUBLE NOT NULL DEFAULT 0 AFTER avg_cpu_percent"},
		{"task_timeseries", "avg_mem_percent", "ALTER TABLE task_timeseries ADD COLUMN avg_mem_percent DOUBLE NOT NULL DEFAULT 0 AFTER max_cpu_percent"},
		{"task_timeseries", "max_mem_percent", "ALTER TABLE task_timeseries ADD COLUMN max_mem_percent DOUBLE NOT NULL DEFAULT 0 AFTER avg_mem_percent"},
		{"task_timeseries", "goroutines", "ALTER TABLE task_timeseries ADD COLUMN goroutines INT NOT NULL DEFAULT 0 AFTER max_mem_percent"},
		{"task_timeseries", "threads", "ALTER TABLE task_timeseries ADD COLUMN threads INT NOT NULL DEFAULT 0 AFTER goroutines"},
		{"task_timeseries", "fds", "ALTER TABLE task_timeseries ADD COLUMN fds INT NOT NULL DEFAULT 0 AFTER threads"},
		{"task_timeseries", "online_count", "ALTER TABLE task_timeseries ADD COLUMN online_count INT NOT NULL DEFAULT 0 AFTER fds"},
		{"task_timeseries", "offline_count", "ALTER TABLE task_timeseries ADD COLUMN offline_count INT NOT NULL DEFAULT 0 AFTER online_count"},
	}
	for _, column := range columns {
		if err := addColumnIfMissing(ctx, db, database, column.table, column.column, column.ddl); err != nil {
			return err
		}
	}

	// MySQL DDL 会隐式提交。先以可空列加入，再回填并收紧约束，使中断后的重跑可以继续前向完成。
	if _, err := db.ExecContext(ctx, `
		UPDATE task_timeseries
		SET window_from = COALESCE(window_from, sampled_at),
			window_to = COALESCE(window_to, sampled_at),
			history_batch_token = COALESCE(history_batch_token, UNHEX(SHA2(CONCAT(task_id, ':', id), 256))),
			sample_count = COALESCE(sample_count, 1)
		WHERE window_from IS NULL OR window_to IS NULL
			OR history_batch_token IS NULL OR sample_count IS NULL`); err != nil {
		return fmt.Errorf("回填历史时序迁移列: %w", err)
	}
	for _, item := range []struct {
		column string
		ddl    string
	}{
		{"window_from", "ALTER TABLE task_timeseries MODIFY COLUMN window_from DATETIME(6) NOT NULL"},
		{"window_to", "ALTER TABLE task_timeseries MODIFY COLUMN window_to DATETIME(6) NOT NULL"},
		{"history_batch_token", "ALTER TABLE task_timeseries MODIFY COLUMN history_batch_token BINARY(32) NOT NULL"},
		{"sample_count", "ALTER TABLE task_timeseries MODIFY COLUMN sample_count BIGINT NOT NULL"},
	} {
		if err := alterColumnNullability(ctx, db, database, "task_timeseries", item.column, false, item.ddl); err != nil {
			return err
		}
	}

	// 旧指标列曾使用 NOT NULL DEFAULT 0，但当前用 NULL 表示“无样本/不适用”；统一放宽，
	// 同时保留最早期 JSON 载荷列的数据，避免这些无默认值的遗留列阻断当前列式采样写入。
	for _, item := range []struct {
		column string
		ddl    string
	}{
		{"rtt_apdex", "ALTER TABLE task_timeseries MODIFY COLUMN rtt_apdex DOUBLE NULL"},
		{"listen_wait_p99_ms", "ALTER TABLE task_timeseries MODIFY COLUMN listen_wait_p99_ms DOUBLE NULL"},
		{"rtt_avg_ms", "ALTER TABLE task_timeseries MODIFY COLUMN rtt_avg_ms DOUBLE NULL"},
		{"rtt_p50_ms", "ALTER TABLE task_timeseries MODIFY COLUMN rtt_p50_ms DOUBLE NULL"},
		{"rtt_p90_ms", "ALTER TABLE task_timeseries MODIFY COLUMN rtt_p90_ms DOUBLE NULL"},
		{"rtt_p95_ms", "ALTER TABLE task_timeseries MODIFY COLUMN rtt_p95_ms DOUBLE NULL"},
		{"rtt_p99_ms", "ALTER TABLE task_timeseries MODIFY COLUMN rtt_p99_ms DOUBLE NULL"},
		{"active_connections", "ALTER TABLE task_timeseries MODIFY COLUMN active_connections BIGINT NULL"},
		{"closed_connections", "ALTER TABLE task_timeseries MODIFY COLUMN closed_connections BIGINT NULL"},
		{"dropped_connections", "ALTER TABLE task_timeseries MODIFY COLUMN dropped_connections BIGINT NULL"},
		{"net_send_bytes_per_sec", "ALTER TABLE task_timeseries MODIFY COLUMN net_send_bytes_per_sec DOUBLE NULL"},
		{"net_recv_bytes_per_sec", "ALTER TABLE task_timeseries MODIFY COLUMN net_recv_bytes_per_sec DOUBLE NULL"},
		{"assigned_agents", "ALTER TABLE task_timeseries MODIFY COLUMN assigned_agents BIGINT NULL"},
		{"reporting_agents", "ALTER TABLE task_timeseries MODIFY COLUMN reporting_agents BIGINT NULL"},
		{"reporting_coverage", "ALTER TABLE task_timeseries MODIFY COLUMN reporting_coverage DOUBLE NULL"},
		{"total_duration_avg_ms", "ALTER TABLE task_timeseries MODIFY COLUMN total_duration_avg_ms DOUBLE NULL"},
		{"total_duration_p95_ms", "ALTER TABLE task_timeseries MODIFY COLUMN total_duration_p95_ms DOUBLE NULL"},
		{"total_duration_p99_ms", "ALTER TABLE task_timeseries MODIFY COLUMN total_duration_p99_ms DOUBLE NULL"},
		{"client_avg_ms", "ALTER TABLE task_timeseries MODIFY COLUMN client_avg_ms DOUBLE NULL"},
		{"encode_avg_ms", "ALTER TABLE task_timeseries MODIFY COLUMN encode_avg_ms DOUBLE NULL"},
		{"decode_avg_ms", "ALTER TABLE task_timeseries MODIFY COLUMN decode_avg_ms DOUBLE NULL"},
		{"data_type", "ALTER TABLE task_timeseries MODIFY COLUMN data_type VARCHAR(32) NULL"},
		{"snapshot", "ALTER TABLE task_timeseries MODIFY COLUMN snapshot JSON NULL"},
	} {
		if err := alterColumnNullability(ctx, db, database, "task_timeseries", item.column, true, item.ddl); err != nil {
			return err
		}
	}

	for _, index := range []struct {
		table string
		name  string
		ddl   string
	}{
		{"task_history", "idx_started", "ALTER TABLE task_history ADD INDEX idx_started (started_at)"},
		{"task_history", "idx_stopped", "ALTER TABLE task_history ADD INDEX idx_stopped (stopped_at)"},
		{"task_report", "idx_task_stage", "ALTER TABLE task_report ADD INDEX idx_task_stage (task_id, stage_index)"},
		{"task_timeseries", "idx_task_elapsed", "ALTER TABLE task_timeseries ADD INDEX idx_task_elapsed (task_id, elapsed_sec)"},
		{"task_timeseries", "idx_task_stage_elapsed", "ALTER TABLE task_timeseries ADD INDEX idx_task_stage_elapsed (task_id, stage_index, elapsed_sec)"},
		{"task_timeseries", "uq_task_history_batch", "ALTER TABLE task_timeseries ADD UNIQUE KEY uq_task_history_batch (task_id, history_batch_token)"},
	} {
		if err := addIndexIfMissing(ctx, db, database, index.table, index.name, index.ddl); err != nil {
			return err
		}
	}
	if err := ensurePrimaryKey(ctx, db, database, "task_aggregated", []string{"task_id", "stage_index"},
		"ALTER TABLE task_aggregated ADD PRIMARY KEY (task_id, stage_index)"); err != nil {
		return err
	}

	// 旧版本把收藏/标签/备注放在 task_history；只补不存在的 task_meta 行，不覆盖新系统已编辑的数据。
	legacyMeta := true
	for _, column := range []string{"starred", "tags", "note"} {
		exists, err := columnExists(ctx, db, database, "task_history", column)
		if err != nil {
			return err
		}
		legacyMeta = legacyMeta && exists
	}
	if legacyMeta {
		if _, err := db.ExecContext(ctx, `
			INSERT IGNORE INTO task_meta (task_id, stage_index, starred, tags, note, updated_at)
			SELECT id, -1, starred, tags, note, UTC_TIMESTAMP(3)
			FROM task_history
			WHERE starred <> 0 OR tags IS NOT NULL OR note IS NOT NULL`); err != nil {
			return fmt.Errorf("迁移旧任务元数据: %w", err)
		}
	}
	return nil
}
