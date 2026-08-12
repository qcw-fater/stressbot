package migrations

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/pressly/goose/v3"
)

func TestAddColumnIfMissingExecutesExactlyOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("stressbot", "task_history", "active_agent_count").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`ALTER TABLE task_history ADD COLUMN active_agent_count`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = addColumnIfMissing(context.Background(), db, "stressbot", "task_history", "active_agent_count",
		"ALTER TABLE task_history ADD COLUMN active_agent_count INT NOT NULL DEFAULT 0")
	if err != nil {
		t.Fatalf("addColumnIfMissing() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAddColumnIfMissingSkipsExistingColumn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("stressbot", "task_history", "active_agent_count").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	err = addColumnIfMissing(context.Background(), db, "stressbot", "task_history", "active_agent_count",
		"ALTER TABLE task_history ADD COLUMN active_agent_count INT NOT NULL DEFAULT 0")
	if err != nil {
		t.Fatalf("addColumnIfMissing() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAddIndexIfMissingSkipsExistingIndex(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("stressbot", "task_timeseries", "uq_task_history_batch").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	err = addIndexIfMissing(context.Background(), db, "stressbot", "task_timeseries", "uq_task_history_batch",
		"ALTER TABLE task_timeseries ADD UNIQUE KEY uq_task_history_batch (task_id, history_batch_token)")
	if err != nil {
		t.Fatalf("addIndexIfMissing() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAlterColumnNullabilityRelaxesLegacyMetric(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT IS_NULLABLE, COLLATION_NAME`).
		WithArgs("stressbot", "task_timeseries", "rtt_apdex").
		WillReturnRows(sqlmock.NewRows([]string{"is_nullable", "collation_name"}).AddRow("NO", nil))
	mock.ExpectExec(`ALTER TABLE task_timeseries MODIFY COLUMN rtt_apdex DOUBLE NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = alterColumnNullability(context.Background(), db, "stressbot", "task_timeseries", "rtt_apdex", true,
		"ALTER TABLE task_timeseries MODIFY COLUMN rtt_apdex DOUBLE NULL")
	if err != nil {
		t.Fatalf("alterColumnNullability() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGoMigrationsHaveStableOrderedVersions(t *testing.T) {
	got := GoMigrations()
	if len(got) != 2 || got[0].Version != 2 || got[1].Version != 3 {
		t.Fatalf("GoMigrations() versions = %#v, want [2, 3]", got)
	}
}

func TestBaselineMigrationParsesAndExecutesThroughGoose(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	provider, err := goose.NewProvider(
		goose.DialectMySQL,
		db,
		Files,
		goose.WithDisableGlobalRegistry(true),
		goose.WithDisableVersioning(true),
	)
	if err != nil {
		t.Fatalf("goose.NewProvider() error = %v", err)
	}
	mock.ExpectBegin()
	for range 11 {
		mock.ExpectExec(`CREATE TABLE IF NOT EXISTS`).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS agent_commands`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	results, err := provider.Up(context.Background())
	if err != nil {
		t.Fatalf("Provider.Up() error = %v", err)
	}
	if len(results) != 2 || results[0].Source.Version != 1 || results[1].Source.Version != 4 {
		t.Fatalf("Provider.Up() results = %#v, want SQL migration versions [1, 4]", results)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
