package mysql

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestInitializeSchemaExecutesCurrentStatementsInOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	for _, statement := range currentSchema() {
		mock.ExpectExec(regexp.QuoteMeta(statement.ddl)).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}

	if err := InitializeSchema(context.Background(), db); err != nil {
		t.Fatalf("InitializeSchema: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	for _, statement := range currentSchema() {
		if strings.Contains(strings.ToLower(statement.ddl), "goose_db_version") {
			t.Fatal("首版 schema 不应创建版本记录表")
		}
	}
}

func TestInitializeSchemaRejectsNilDatabase(t *testing.T) {
	if err := InitializeSchema(context.Background(), nil); err == nil {
		t.Fatal("InitializeSchema(nil) 应返回错误")
	}
}

func TestInitializeSchemaExecErrorIncludesTableName(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	first := currentSchema()[0]
	sentinel := errors.New("asserted schema error")
	mock.ExpectExec(regexp.QuoteMeta(first.ddl)).
		WillReturnError(sentinel)

	err = InitializeSchema(context.Background(), db)
	if !errors.Is(err, sentinel) {
		t.Fatalf("InitializeSchema error = %v, want wrapped sentinel", err)
	}
	if !strings.Contains(err.Error(), first.table) {
		t.Fatalf("InitializeSchema error = %v, want table name %q", err, first.table)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentSchemaReturnsIndependentSlice(t *testing.T) {
	first := currentSchema()
	first[0].table = "mutated"

	if got := currentSchema()[0].table; got != "task_history" {
		t.Fatalf("currentSchema()[0].table = %q, want task_history", got)
	}
}
