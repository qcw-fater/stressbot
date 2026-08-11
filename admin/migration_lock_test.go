package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectMigrationLock(mock sqlmock.Sqlmock, value any) {
	mock.ExpectQuery(`SELECT GET_LOCK\('stressbot_schema_migration', \?\)`).
		WithArgs(int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"lock_status"}).AddRow(value))
}

func expectMigrationUnlock(mock sqlmock.Sqlmock, value any) {
	mock.ExpectQuery(`SELECT RELEASE_LOCK\('stressbot_schema_migration'\)`).
		WillReturnRows(sqlmock.NewRows([]string{"lock_status"}).AddRow(value))
}

func TestMigrationLockUsesOneDedicatedSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	expectMigrationLock(mock, int64(1))
	expectMigrationUnlock(mock, int64(1))

	lock, err := acquireMigrationLock(context.Background(), db, 30*time.Second)
	if err != nil {
		t.Fatalf("acquireMigrationLock() error = %v", err)
	}
	if got := db.Stats().InUse; got != 1 {
		t.Fatalf("database in-use connections = %d, want 1", got)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if got := db.Stats().InUse; got != 0 {
		t.Fatalf("database in-use connections after release = %d, want 0", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWithMigrationLockReleasesAfterCallbackFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	expectMigrationLock(mock, int64(1))
	expectMigrationUnlock(mock, int64(1))
	sentinel := errors.New("migration failed")

	err = withMigrationLock(context.Background(), db, 30*time.Second, func(context.Context) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("withMigrationLock() error = %v, want %v", err, sentinel)
	}
	if got := db.Stats().InUse; got != 0 {
		t.Fatalf("database in-use connections = %d, want 0", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWithMigrationLockReleasesAfterContextCancellation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	expectMigrationLock(mock, int64(1))
	expectMigrationUnlock(mock, int64(1))
	ctx, cancel := context.WithCancel(context.Background())

	err = withMigrationLock(ctx, db, 30*time.Second, func(context.Context) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("withMigrationLock() error = %v, want context.Canceled", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationLockTimeoutClosesDedicatedSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	expectMigrationLock(mock, int64(0))

	_, err = acquireMigrationLock(context.Background(), db, 30*time.Second)
	if !errors.Is(err, ErrMigrationLockTimeout) {
		t.Fatalf("acquireMigrationLock() error = %v, want ErrMigrationLockTimeout", err)
	}
	if got := db.Stats().InUse; got != 0 {
		t.Fatalf("database in-use connections = %d, want 0", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationLockNullResultIsRejected(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	expectMigrationLock(mock, nil)

	_, err = acquireMigrationLock(context.Background(), db, 30*time.Second)
	if err == nil {
		t.Fatal("acquireMigrationLock() error = nil, want NULL result error")
	}
	if got := db.Stats().InUse; got != 0 {
		t.Fatalf("database in-use connections = %d, want 0", got)
	}
}
