package mysql

import (
	"strings"
	"testing"
)

func normalizeDDL(sqlText string) string {
	sqlText = strings.TrimSuffix(strings.TrimSpace(sqlText), ";")
	return strings.ToLower(strings.Join(strings.Fields(sqlText), " "))
}

func schemaDDL(t *testing.T) ([]string, map[string]string) {
	t.Helper()
	statements := currentSchema()
	ordered := make([]string, 0, len(statements))
	byTable := make(map[string]string, len(statements))
	for _, statement := range statements {
		ordered = append(ordered, statement.table)
		byTable[statement.table] = normalizeDDL(statement.ddl)
	}
	return ordered, byTable
}

func TestCurrentSchemaStatementsIdentifyTheirTables(t *testing.T) {
	seenTables := make(map[string]struct{})
	for _, statement := range currentSchema() {
		if statement.table == "" {
			t.Fatal("schema statement table 不能为空")
		}
		if _, ok := seenTables[statement.table]; ok {
			t.Fatalf("重复的 schema 表名: %q", statement.table)
		}
		seenTables[statement.table] = struct{}{}
		if !strings.HasPrefix(normalizeDDL(statement.ddl), "create table if not exists "+statement.table+" ") {
			t.Errorf("schema statement %q DDL does not create its table: %s", statement.table, statement.ddl)
		}
	}
}

func TestSchemaContainsCurrentTablesInStableOrder(t *testing.T) {
	ordered, _ := schemaDDL(t)
	want := []string{
		"task_history", "task_assignment", "task_report", "task_aggregated", "task_timeseries",
		"task_config_archive", "task_agent_events", "task_meta", "flow_template", "action_template", "listen_template",
	}
	if strings.Join(ordered, ",") != strings.Join(want, ",") {
		t.Fatalf("baseline table order = %v, want %v", ordered, want)
	}
}

func TestTimeseriesDDLContainsWindowAccuracyContract(t *testing.T) {
	_, tables := schemaDDL(t)
	normalized := tables["task_timeseries"]
	for _, fragment := range []string{
		"window_from datetime(6) not null",
		"window_to datetime(6) not null",
		"history_batch_token binary(32) not null",
		"sample_count bigint not null",
		"rtt_p50_ms double null",
		"rtt_p90_ms double null",
		"active_connections bigint null",
		"net_send_bytes_per_sec double null",
		"unique key uq_task_history_batch (task_id, history_batch_token)",
	} {
		if !strings.Contains(normalized, fragment) {
			t.Errorf("task_timeseries DDL missing %q", fragment)
		}
	}
}

func TestTemplateDDLUsesIndependentBinaryUniqueNames(t *testing.T) {
	_, tables := schemaDDL(t)
	for table, fragments := range map[string][]string{
		"action_template": {
			"name varchar(80) character set utf8mb4 collate utf8mb4_bin not null",
			"unique index uq_action_template_name (name)",
		},
		"listen_template": {
			"name varchar(80) character set utf8mb4 collate utf8mb4_bin not null",
			"unique index uq_listen_template_name (name)",
		},
	} {
		for _, fragment := range fragments {
			if !strings.Contains(tables[table], fragment) {
				t.Errorf("%s DDL missing %q: %s", table, fragment, tables[table])
			}
		}
	}
}
