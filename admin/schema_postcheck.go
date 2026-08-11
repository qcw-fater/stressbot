package admin

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

var requiredSchemaColumns = map[string][]string{
	"task_history":        {"id", "active_agent_count", "flow_template_id"},
	"task_report":         {"task_id", "cleanup_status", "stage_index"},
	"task_aggregated":     {"task_id", "stage_index"},
	"task_timeseries":     {"task_id", "stage_index", "window_from", "window_to", "history_batch_token", "sample_count", "rtt_p50_ms", "rtt_p90_ms"},
	"task_config_archive": {"task_id", "codecs", "error_map"},
	"task_meta":           {"task_id", "stage_index"},
	"flow_template":       {"id", "flow_json"},
	"action_template":     {"id", "name", "data_json"},
	"listen_template":     {"id", "name", "data_json"},
}

type schemaIndex struct {
	unique  bool
	columns []string
}

type schemaSnapshot struct {
	columns map[string]map[string]string
	indexes map[string]map[string]schemaIndex
}

func newSchemaSnapshot() schemaSnapshot {
	return schemaSnapshot{
		columns: make(map[string]map[string]string),
		indexes: make(map[string]map[string]schemaIndex),
	}
}

func (s schemaSnapshot) addColumn(table, column, collation string) {
	if s.columns[table] == nil {
		s.columns[table] = make(map[string]string)
	}
	s.columns[table][column] = collation
}

func (s schemaSnapshot) addIndex(table, name string, unique bool, columns ...string) {
	if s.indexes[table] == nil {
		s.indexes[table] = make(map[string]schemaIndex)
	}
	s.indexes[table][name] = schemaIndex{unique: unique, columns: append([]string(nil), columns...)}
}

func (s schemaSnapshot) appendIndexColumn(table, name string, unique bool, column string) {
	if s.indexes[table] == nil {
		s.indexes[table] = make(map[string]schemaIndex)
	}
	index := s.indexes[table][name]
	index.unique = unique
	index.columns = append(index.columns, column)
	s.indexes[table][name] = index
}

func validateSchemaSnapshot(snapshot schemaSnapshot) error {
	var problems []string
	for table, columns := range requiredSchemaColumns {
		for _, column := range columns {
			if _, ok := snapshot.columns[table][column]; !ok {
				problems = append(problems, "缺少必需列 "+table+"."+column)
			}
		}
	}

	for _, contract := range []struct {
		table   string
		name    string
		columns []string
	}{
		{"task_timeseries", "uq_task_history_batch", []string{"task_id", "history_batch_token"}},
		{"task_meta", "PRIMARY", []string{"task_id", "stage_index"}},
		{"task_aggregated", "PRIMARY", []string{"task_id", "stage_index"}},
		{"action_template", "uq_action_template_name", []string{"name"}},
		{"listen_template", "uq_listen_template_name", []string{"name"}},
	} {
		index, ok := snapshot.indexes[contract.table][contract.name]
		if !ok || !index.unique || !slices.Equal(index.columns, contract.columns) {
			problems = append(problems, fmt.Sprintf(
				"索引 %s.%s 不符合唯一列契约 %v", contract.table, contract.name, contract.columns))
		}
	}

	for _, table := range []string{"action_template", "listen_template"} {
		if collation := snapshot.columns[table]["name"]; !strings.EqualFold(collation, "utf8mb4_bin") {
			problems = append(problems, table+".name 必须使用 utf8mb4_bin 排序规则")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("数据库 schema post-check 失败: %s", strings.Join(problems, "; "))
	}
	return nil
}

func loadSchemaSnapshot(ctx context.Context, db *sql.DB, database string) (schemaSnapshot, error) {
	snapshot := newSchemaSnapshot()
	columns, err := db.QueryContext(ctx, `
		SELECT TABLE_NAME, COLUMN_NAME, COLLATION_NAME
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ?`, database)
	if err != nil {
		return snapshot, fmt.Errorf("读取 schema 列信息: %w", err)
	}
	for columns.Next() {
		var table, column string
		var collation sql.NullString
		if err := columns.Scan(&table, &column, &collation); err != nil {
			_ = columns.Close()
			return snapshot, fmt.Errorf("解析 schema 列信息: %w", err)
		}
		snapshot.addColumn(table, column, collation.String)
	}
	if err := columns.Err(); err != nil {
		_ = columns.Close()
		return snapshot, fmt.Errorf("遍历 schema 列信息: %w", err)
	}
	if err := columns.Close(); err != nil {
		return snapshot, fmt.Errorf("关闭 schema 列结果集: %w", err)
	}

	indexes, err := db.QueryContext(ctx, `
		SELECT TABLE_NAME, INDEX_NAME, NON_UNIQUE, COLUMN_NAME
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ?
		ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, database)
	if err != nil {
		return snapshot, fmt.Errorf("读取 schema 索引信息: %w", err)
	}
	for indexes.Next() {
		var table, name, column string
		var nonUnique int
		if err := indexes.Scan(&table, &name, &nonUnique, &column); err != nil {
			_ = indexes.Close()
			return snapshot, fmt.Errorf("解析 schema 索引信息: %w", err)
		}
		snapshot.appendIndexColumn(table, name, nonUnique == 0, column)
	}
	if err := indexes.Err(); err != nil {
		_ = indexes.Close()
		return snapshot, fmt.Errorf("遍历 schema 索引信息: %w", err)
	}
	if err := indexes.Close(); err != nil {
		return snapshot, fmt.Errorf("关闭 schema 索引结果集: %w", err)
	}
	return snapshot, nil
}

func postCheckMySQLSchema(ctx context.Context, db *sql.DB) error {
	var database sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		return fmt.Errorf("查询当前数据库: %w", err)
	}
	if !database.Valid || strings.TrimSpace(database.String) == "" {
		return fmt.Errorf("数据库 schema post-check 失败: 当前连接未选择数据库")
	}
	snapshot, err := loadSchemaSnapshot(ctx, db, database.String)
	if err != nil {
		return err
	}
	return validateSchemaSnapshot(snapshot)
}
