package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	json "stressbot/utils/jsonx"
)

func TestComputeFlowSnapshotRevisionIsOrderIndependent(t *testing.T) {
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	a := FlowTemplateDetail{
		FlowTemplateSummary: FlowTemplateSummary{ID: "a", Name: "A", CreatedAt: now, UpdatedAt: now},
		Flow:                json.RawMessage(`{"defaultDelayMs":1000,"nodes":{},"actions":{},"listens":{}}`),
	}
	b := FlowTemplateDetail{
		FlowTemplateSummary: FlowTemplateSummary{ID: "b", Name: "B", CreatedAt: now, UpdatedAt: now},
		Flow:                json.RawMessage(`{"defaultDelayMs":1000,"nodes":{},"actions":{},"listens":{}}`),
	}

	r1, err := computeFlowSnapshotRevision([]FlowTemplateDetail{a, b})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := computeFlowSnapshotRevision([]FlowTemplateDetail{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if r1 != r2 {
		t.Fatalf("revision differs: %q != %q", r1, r2)
	}
}

func TestValidateFlowSnapshotItemsRejectsDuplicateID(t *testing.T) {
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	items := []FlowTemplateDetail{
		{
			FlowTemplateSummary: FlowTemplateSummary{ID: "same", Name: "A", CreatedAt: now, UpdatedAt: now},
			Flow:                json.RawMessage(`{"nodes":{},"actions":{}}`),
		},
		{
			FlowTemplateSummary: FlowTemplateSummary{ID: "same", Name: "B", CreatedAt: now, UpdatedAt: now},
			Flow:                json.RawMessage(`{"nodes":{},"actions":{}}`),
		},
	}

	err := validateFlowSnapshotItems(items)
	if err == nil || !strings.Contains(err.Error(), "流程 ID 重复") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateFlowSnapshotItemsRejectsMissingTimestamps(t *testing.T) {
	items := []FlowTemplateDetail{{
		FlowTemplateSummary: FlowTemplateSummary{ID: "flow-1", Name: "Flow"},
		Flow:                json.RawMessage(`{"nodes":{},"actions":{}}`),
	}}

	err := validateFlowSnapshotItems(items)
	if err == nil || !strings.Contains(err.Error(), "时间无效") {
		t.Fatalf("error = %v", err)
	}
}

func TestReplaceSnapshotUsesOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewFlowTemplateStore(db)
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	current := FlowTemplateDetail{
		FlowTemplateSummary: FlowTemplateSummary{ID: "old", Name: "Old", CreatedAt: now, UpdatedAt: now},
		Flow:                json.RawMessage(`{"nodes":{},"actions":{}}`),
	}
	before, err := computeFlowSnapshotRevision([]FlowTemplateDetail{current})
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, name, node_count.*FROM flow_template`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "node_count", "action_count", "created_at", "updated_at", "flow_json", "layout_json",
		}).AddRow("old", "Old", 0, 0, now, now, []byte(`{"nodes":{},"actions":{}}`), nil))
	mock.ExpectExec(`DELETE FROM flow_template`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO flow_template`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	resp, err := store.ReplaceSnapshot(context.Background(), ReplaceFlowSnapshotRequest{
		ExpectedRevision: before,
		Items: []FlowTemplateDetail{{
			FlowTemplateSummary: FlowTemplateSummary{ID: "new", Name: "New", CreatedAt: now, UpdatedAt: now},
			Flow:                json.RawMessage(`{"nodes":{},"actions":{}}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 {
		t.Fatalf("count = %d", resp.Count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceSnapshotRejectsStaleRevisionWithoutWriting(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewFlowTemplateStore(db)
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, name, node_count.*FROM flow_template`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "node_count", "action_count", "created_at", "updated_at", "flow_json", "layout_json",
		}).AddRow("old", "Old", 0, 0, now, now, []byte(`{"nodes":{},"actions":{}}`), nil))
	mock.ExpectRollback()

	_, err = store.ReplaceSnapshot(context.Background(), ReplaceFlowSnapshotRequest{
		ExpectedRevision: "sha256:stale",
		Items: []FlowTemplateDetail{{
			FlowTemplateSummary: FlowTemplateSummary{ID: "new", Name: "New", CreatedAt: now, UpdatedAt: now},
			Flow:                json.RawMessage(`{"nodes":{},"actions":{}}`),
		}},
	})
	apiErr, ok := err.(*Error)
	if !ok || apiErr.Code != "FLOW_SNAPSHOT_CONFLICT" {
		t.Fatalf("error = %#v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
