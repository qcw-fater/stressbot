package admin

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

type fakeMigrationProvider struct {
	upResults    []*goose.MigrationResult
	upErr        error
	byOneResult  *goose.MigrationResult
	byOneErr     error
	statusResult []*goose.MigrationStatus
	statusErr    error
}

func (f *fakeMigrationProvider) Up(context.Context) ([]*goose.MigrationResult, error) {
	return f.upResults, f.upErr
}

func (f *fakeMigrationProvider) UpByOne(context.Context) (*goose.MigrationResult, error) {
	return f.byOneResult, f.byOneErr
}

func (f *fakeMigrationProvider) Status(context.Context) ([]*goose.MigrationStatus, error) {
	return f.statusResult, f.statusErr
}

func TestExecuteMigrationCommandDoesNotPostCheckFailedUp(t *testing.T) {
	sentinel := errors.New("migration step failed")
	postChecked := false
	err := executeMigrationCommand(context.Background(), &fakeMigrationProvider{upErr: sentinel}, MigrationAuto, nil,
		func(context.Context) error {
			postChecked = true
			return nil
		})
	if !errors.Is(err, sentinel) {
		t.Fatalf("executeMigrationCommand() error = %v, want %v", err, sentinel)
	}
	if postChecked {
		t.Fatal("post-check ran after failed migration")
	}
}

func TestExecuteMigrationCommandPropagatesPostCheckFailure(t *testing.T) {
	sentinel := errors.New("schema incomplete")
	err := executeMigrationCommand(context.Background(), &fakeMigrationProvider{}, MigrationUp, nil,
		func(context.Context) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("executeMigrationCommand() error = %v, want %v", err, sentinel)
	}
}

func TestExecuteMigrationStatusDoesNotMutateOrPostCheck(t *testing.T) {
	provider := &fakeMigrationProvider{statusResult: []*goose.MigrationStatus{
		{
			Source:    &goose.Source{Version: 1, Path: "00001_current_schema.sql"},
			State:     goose.StateApplied,
			AppliedAt: time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC),
		},
		{
			Source: &goose.Source{Version: 2, Path: "00002_reconcile_history.go"},
			State:  goose.StatePending,
		},
	}}
	var output bytes.Buffer
	postChecked := false
	err := executeMigrationCommand(context.Background(), provider, MigrationStatus, &output,
		func(context.Context) error {
			postChecked = true
			return nil
		})
	if err != nil {
		t.Fatalf("executeMigrationCommand() error = %v", err)
	}
	if postChecked {
		t.Fatal("status command ran post-check")
	}
	for _, want := range []string{"00001", "applied", "00002", "pending"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("status output %q missing %q", output.String(), want)
		}
	}
}

func TestExecuteMigrationCommandRejectsDownOperations(t *testing.T) {
	err := executeMigrationCommand(context.Background(), &fakeMigrationProvider{}, MigrationCommand("down"), nil, nil)
	if err == nil {
		t.Fatal("executeMigrationCommand() error = nil, want unsupported command error")
	}
}

func TestExecuteMigrationUpByOneTreatsNoPendingAsSuccess(t *testing.T) {
	var output bytes.Buffer
	err := executeMigrationCommand(context.Background(), &fakeMigrationProvider{byOneErr: goose.ErrNoNextVersion},
		MigrationUpByOne, &output, nil)
	if err != nil {
		t.Fatalf("executeMigrationCommand() error = %v", err)
	}
	if !strings.Contains(output.String(), "没有待执行") {
		t.Fatalf("output = %q, want no pending message", output.String())
	}
}

func TestParseMigrationCommandAllowsOnlyForwardOperations(t *testing.T) {
	for _, raw := range []string{"", "auto", "status", "up", "up-by-one", " UP "} {
		if _, err := ParseMigrationCommand(raw); err != nil {
			t.Errorf("ParseMigrationCommand(%q) error = %v", raw, err)
		}
	}
	for _, raw := range []string{"down", "reset", "redo"} {
		if _, err := ParseMigrationCommand(raw); err == nil {
			t.Errorf("ParseMigrationCommand(%q) error = nil, want rejection", raw)
		}
	}
}
