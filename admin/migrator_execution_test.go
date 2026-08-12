package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

type fakeMigrationProvider struct {
	upResults []*goose.MigrationResult
	upErr     error
}

func (f *fakeMigrationProvider) Up(context.Context) ([]*goose.MigrationResult, error) {
	return f.upResults, f.upErr
}

func TestExecuteMigrationsDoesNotPostCheckFailedUp(t *testing.T) {
	sentinel := errors.New("migration step failed")
	postChecked := false
	_, err := executeMigrations(context.Background(), &fakeMigrationProvider{upErr: sentinel},
		func(context.Context) error {
			postChecked = true
			return nil
		})
	if !errors.Is(err, sentinel) {
		t.Fatalf("executeMigrations() error = %v, want %v", err, sentinel)
	}
	if postChecked {
		t.Fatal("post-check ran after failed migration")
	}
}

func TestExecuteMigrationsPropagatesPostCheckFailure(t *testing.T) {
	sentinel := errors.New("schema incomplete")
	_, err := executeMigrations(context.Background(), &fakeMigrationProvider{},
		func(context.Context) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("executeMigrations() error = %v, want %v", err, sentinel)
	}
}

func TestExecuteMigrationsReturnsAppliedResults(t *testing.T) {
	want := []*goose.MigrationResult{{Source: &goose.Source{Version: 1, Path: "00001_current_schema.sql"}}}
	got, err := executeMigrations(context.Background(), &fakeMigrationProvider{upResults: want}, nil)
	if err != nil {
		t.Fatalf("executeMigrations() error = %v", err)
	}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("executeMigrations() results = %#v, want %#v", got, want)
	}
}

func TestMigrationSummaryMessageReportsLatestSchema(t *testing.T) {
	if got := migrationSummaryMessage(0); !strings.Contains(got, "已是最新版本") {
		t.Fatalf("migrationSummaryMessage(0) = %q, want latest schema message", got)
	}
}

func TestMigrationSummaryMessageReportsAppliedCount(t *testing.T) {
	if got := migrationSummaryMessage(3); !strings.Contains(got, "3") {
		t.Fatalf("migrationSummaryMessage(3) = %q, want applied count", got)
	}
}
