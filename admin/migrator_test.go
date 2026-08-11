package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/pressly/goose/v3"
)

func TestNewMigrationProviderRejectsMissingMigrations(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = newMigrationProvider(goose.DialectMySQL, db, fstest.MapFS{}, nil)
	if !errors.Is(err, goose.ErrNoMigrations) {
		t.Fatalf("newMigrationProvider() error = %v, want ErrNoMigrations", err)
	}
}

func TestNewMigrationProviderRejectsDuplicateVersions(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	files := fstest.MapFS{
		"00001_first.sql":  &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n")},
		"00001_second.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n")},
	}
	_, err = newMigrationProvider(goose.DialectMySQL, db, files, nil)
	if err == nil {
		t.Fatal("newMigrationProvider() error = nil, want duplicate version error")
	}
}

func TestNewMigrationProviderRejectsUnknownDialect(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	files := fstest.MapFS{
		"00001_first.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n")},
	}
	_, err = newMigrationProvider(goose.Dialect("unknown"), db, files, nil)
	if err == nil {
		t.Fatal("newMigrationProvider() error = nil, want unknown dialect error")
	}
}

func TestNewMigrationProviderUsesOnlyExplicitGoMigrations(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	files := fstest.MapFS{
		"00001_first.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n")},
	}
	explicit := goose.NewGoMigration(2, &goose.GoFunc{RunDB: func(_ context.Context, _ *sql.DB) error {
		return nil
	}}, nil)
	provider, err := newMigrationProvider(goose.DialectMySQL, db, files, []*goose.Migration{explicit})
	if err != nil {
		t.Fatalf("newMigrationProvider() error = %v", err)
	}
	sources := provider.ListSources()
	if len(sources) != 2 || sources[0].Version != 1 || sources[1].Version != 2 {
		t.Fatalf("sources = %#v, want explicit versions [1, 2]", sources)
	}
}
