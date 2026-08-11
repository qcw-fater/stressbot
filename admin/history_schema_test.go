package admin

import (
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"stressbot/admin/migrations"
)

func normalizeDDL(sqlText string) string {
	sqlText = strings.TrimSuffix(strings.TrimSpace(sqlText), ";")
	return strings.ToLower(strings.Join(strings.Fields(sqlText), " "))
}

func baselineDDL(t *testing.T) ([]string, map[string]string) {
	t.Helper()
	raw, err := fs.ReadFile(migrations.Files, "00001_current_schema.sql")
	if err != nil {
		t.Fatalf("read baseline migration: %v", err)
	}
	up := strings.SplitN(string(raw), "-- +goose Down", 2)[0]
	var ordered []string
	byTable := make(map[string]string)
	for _, statement := range strings.Split(up, ";") {
		statement = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(statement), "-- +goose Up"))
		if strings.HasPrefix(strings.ToUpper(statement), "CREATE TABLE") {
			normalized := normalizeDDL(statement)
			fields := strings.Fields(normalized)
			if len(fields) < 6 {
				t.Fatalf("invalid baseline CREATE TABLE: %s", normalized)
			}
			table := fields[5]
			ordered = append(ordered, table)
			byTable[table] = normalized
		}
	}
	return ordered, byTable
}

func TestBaselineMigrationContainsCurrentTablesInStableOrder(t *testing.T) {
	ordered, _ := baselineDDL(t)
	want := []string{
		"task_history", "task_assignment", "task_report", "task_aggregated", "task_timeseries",
		"task_config_archive", "task_agent_events", "task_meta", "flow_template", "action_template", "listen_template",
	}
	if strings.Join(ordered, ",") != strings.Join(want, ",") {
		t.Fatalf("baseline table order = %v, want %v", ordered, want)
	}
}

func TestTimeseriesDDLContainsWindowAccuracyContract(t *testing.T) {
	_, tables := baselineDDL(t)
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

func TestTaskConfigArchiveUpsertUpdatesEveryConfigColumn(t *testing.T) {
	normalized := strings.ToLower(strings.Join(strings.Fields(upsertTaskConfigArchiveSQL), " "))
	for _, column := range []string{
		"flow_json",
		"proto_files",
		"lua_scripts",
		"codecs",
		"error_map",
		"robot_config",
	} {
		want := column + "=values(" + column + ")"
		if !strings.Contains(normalized, want) {
			t.Errorf("UPSERT does not update %s", column)
		}
	}
}

func TestTemplateDDLUsesIndependentBinaryUniqueNames(t *testing.T) {
	_, tables := baselineDDL(t)
	for name, ddl := range map[string]string{
		"action": tables["action_template"],
		"listen": tables["listen_template"],
	} {
		normalized := strings.ToLower(strings.Join(strings.Fields(ddl), " "))
		for _, fragment := range []string{
			"engine=innodb",
			"collate utf8mb4_bin",
			"unique index",
			"created_at datetime(3)",
			"updated_at datetime(3)",
		} {
			if !strings.Contains(normalized, fragment) {
				t.Errorf("%s DDL missing %q: %s", name, fragment, normalized)
			}
		}
	}
}

func TestTemplateErrorsHaveStableStatusCodes(t *testing.T) {
	checks := map[*Error]int{
		ErrTemplateLibraryDisabled:  http.StatusServiceUnavailable,
		ErrActionTemplateNotFound:   http.StatusNotFound,
		ErrListenTemplateNotFound:   http.StatusNotFound,
		ErrTemplateNameConflict:     http.StatusConflict,
		ErrTemplateSnapshotConflict: http.StatusConflict,
	}
	for apiErr, want := range checks {
		if apiErr.HTTPStatus != want {
			t.Errorf("%s status=%d want=%d", apiErr.Code, apiErr.HTTPStatus, want)
		}
	}
}
