package template

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"go.uber.org/zap"

	json "stressbot/internal/jsonx"
	"stressbot/internal/stresslog"
)

func snapshotAction(id, name string, createdAt, updatedAt time.Time) ActionTemplate {
	return ActionTemplate{ID: id, Name: name, Pattern: "tcpRequest", Data: json.RawMessage(`{"pattern":"tcpRequest","service":"logic","route":{"cmd":1},"s2cProto":"LoginS2C"}`), CreatedAt: createdAt, UpdatedAt: updatedAt}
}

func TestTemplateSnapshotRevisionIsOrderIndependent(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	a, b := snapshotAction("a", "A", now, now), snapshotAction("b", "B", now, now)
	left, err := computeActionSnapshotRevision([]ActionTemplate{a, b})
	if err != nil {
		t.Fatal(err)
	}
	right, err := computeActionSnapshotRevision([]ActionTemplate{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("revision differs: %s != %s", left, right)
	}
}

func TestActionSnapshotPreserveKeepsIDAndTimes(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 123000000, time.UTC)
	incoming := []ActionTemplate{snapshotAction("keep", "A", now, now)}
	prepared, err := prepareActionSnapshotItems(nil, incoming, IDPreserve, now.Add(time.Hour), func() string { return "generated" })
	if err != nil {
		t.Fatal(err)
	}
	if prepared[0].ID != "keep" || !prepared[0].CreatedAt.Equal(now) || !prepared[0].UpdatedAt.Equal(now) {
		t.Fatalf("prepared = %#v", prepared[0])
	}
}

func TestActionSnapshotGenerateMissingCreatesServerID(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	incoming := snapshotAction("", "A", time.Time{}, time.Time{})
	prepared, err := prepareActionSnapshotItems(nil, []ActionTemplate{incoming}, IDGenerateMissing, now, func() string { return "server-id" })
	if err != nil {
		t.Fatal(err)
	}
	if prepared[0].ID != "server-id" || !prepared[0].CreatedAt.Equal(now) || !prepared[0].UpdatedAt.Equal(now) {
		t.Fatalf("prepared = %#v", prepared[0])
	}
}

func TestActionSnapshotGenerateMissingPreservesUnchangedTimes(t *testing.T) {
	created := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)
	current := snapshotAction("same", "A", created, updated)
	incoming := current
	incoming.CreatedAt, incoming.UpdatedAt = time.Time{}, time.Time{}
	prepared, err := prepareActionSnapshotItems([]ActionTemplate{current}, []ActionTemplate{incoming}, IDGenerateMissing, updated.Add(time.Hour), testNextID)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared[0].CreatedAt.Equal(created) || !prepared[0].UpdatedAt.Equal(updated) {
		t.Fatalf("prepared = %#v", prepared[0])
	}
}

func TestListenSnapshotGenerateMissingUpdatesChangedExistingItem(t *testing.T) {
	created := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)
	now := updated.Add(time.Hour)
	current := ListenTemplate{ID: "same", Name: "L", Kind: "silent", Data: json.RawMessage(`{}`), CreatedAt: created, UpdatedAt: updated}
	incoming := current
	incoming.Description = "changed"
	incoming.CreatedAt, incoming.UpdatedAt = time.Time{}, time.Time{}
	prepared, err := prepareListenSnapshotItems([]ListenTemplate{current}, []ListenTemplate{incoming}, IDGenerateMissing, now, testNextID)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared[0].CreatedAt.Equal(created) || !prepared[0].UpdatedAt.Equal(now) {
		t.Fatalf("prepared = %#v", prepared[0])
	}
}

func TestTemplateSnapshotRejectsDuplicateNamesBeforeBegin(t *testing.T) {
	now := time.Now()
	items := []ActionTemplate{snapshotAction("a", "Same", now, now), snapshotAction("b", "Same", now, now)}
	if _, err := prepareActionSnapshotItems(nil, items, IDPreserve, now, testNextID); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestTemplateSnapshotRejectsStaleRevisionWithoutDelete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, name, description, pattern.*FROM action_template`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "pattern", "data_json", "created_at", "updated_at"}).
			AddRow("a", "A", nil, "tcpRequest", []byte(`{"pattern":"tcpRequest","service":"logic","route":{"cmd":1},"s2cProto":"LoginS2C"}`), now, now))
	mock.ExpectRollback()
	_, err = NewActionTemplateStore(db, testNextID).ReplaceSnapshot(context.Background(), ReplaceSnapshotRequest[ActionTemplate]{ExpectedRevision: "sha256:stale", IDPolicy: IDPreserve, Items: []ActionTemplate{snapshotAction("a", "A", now, now)}})
	apiErr, ok := errors.AsType[*Error](err)
	if !ok || apiErr.Code != ErrTemplateSnapshotConflict.Code {
		t.Fatalf("error = %#v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTemplateSnapshotRollsBackWhenInsertFails(t *testing.T) {
	if stresslog.GetLogger() == nil {
		stresslog.ReplaceLogger(zap.NewNop())
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	current := snapshotAction("a", "A", now, now)
	revision, _ := computeActionSnapshotRevision([]ActionTemplate{current})
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, name, description, pattern.*FROM action_template`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "pattern", "data_json", "created_at", "updated_at"}).
			AddRow("a", "A", nil, "tcpRequest", []byte(current.Data), now, now))
	mock.ExpectExec(`DELETE FROM action_template`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO action_template`).WillReturnError(errors.New("insert failed"))
	mock.ExpectRollback()
	_, err = NewActionTemplateStore(db, testNextID).ReplaceSnapshot(context.Background(), ReplaceSnapshotRequest[ActionTemplate]{ExpectedRevision: revision, IDPolicy: IDPreserve, Items: []ActionTemplate{current}})
	if err == nil {
		t.Fatal("expected insert error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
