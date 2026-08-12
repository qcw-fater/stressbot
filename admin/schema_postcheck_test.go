package admin

import (
	"strings"
	"testing"
)

func validSchemaSnapshot() schemaSnapshot {
	snapshot := newSchemaSnapshot()
	for table, columns := range requiredSchemaColumns {
		for _, column := range columns {
			snapshot.addColumn(table, column, "")
		}
	}
	snapshot.addColumn("action_template", "name", "utf8mb4_bin")
	snapshot.addColumn("listen_template", "name", "utf8mb4_bin")
	snapshot.addIndex("task_timeseries", "uq_task_history_batch", true, "task_id", "history_batch_token")
	snapshot.addIndex("task_meta", "PRIMARY", true, "task_id", "stage_index")
	snapshot.addIndex("task_aggregated", "PRIMARY", true, "task_id", "stage_index")
	snapshot.addIndex("action_template", "uq_action_template_name", true, "name")
	snapshot.addIndex("listen_template", "uq_listen_template_name", true, "name")
	return snapshot
}

func TestValidateSchemaSnapshotAcceptsRuntimeContract(t *testing.T) {
	if err := validateSchemaSnapshot(validSchemaSnapshot()); err != nil {
		t.Fatalf("validateSchemaSnapshot() error = %v", err)
	}
}

func TestValidateSchemaSnapshotRejectsMissingRequiredColumn(t *testing.T) {
	snapshot := validSchemaSnapshot()
	delete(snapshot.columns["task_history"], "flow_template_id")

	err := validateSchemaSnapshot(snapshot)
	if err == nil || !strings.Contains(err.Error(), "task_history.flow_template_id") {
		t.Fatalf("validateSchemaSnapshot() error = %v, want missing flow_template_id", err)
	}
}

func TestValidateSchemaSnapshotRejectsWrongCompositePrimaryKey(t *testing.T) {
	snapshot := validSchemaSnapshot()
	snapshot.addIndex("task_meta", "PRIMARY", true, "task_id")

	err := validateSchemaSnapshot(snapshot)
	if err == nil || !strings.Contains(err.Error(), "task_meta.PRIMARY") {
		t.Fatalf("validateSchemaSnapshot() error = %v, want task_meta primary key error", err)
	}
}

func TestValidateSchemaSnapshotRejectsNonUniqueHistoryToken(t *testing.T) {
	snapshot := validSchemaSnapshot()
	snapshot.addIndex("task_timeseries", "uq_task_history_batch", false, "task_id", "history_batch_token")

	err := validateSchemaSnapshot(snapshot)
	if err == nil || !strings.Contains(err.Error(), "uq_task_history_batch") {
		t.Fatalf("validateSchemaSnapshot() error = %v, want unique index error", err)
	}
}

func TestValidateSchemaSnapshotRejectsCaseInsensitiveTemplateName(t *testing.T) {
	snapshot := validSchemaSnapshot()
	snapshot.addColumn("action_template", "name", "utf8mb4_general_ci")

	err := validateSchemaSnapshot(snapshot)
	if err == nil || !strings.Contains(err.Error(), "action_template.name") {
		t.Fatalf("validateSchemaSnapshot() error = %v, want binary collation error", err)
	}
}
