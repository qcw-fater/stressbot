package admin

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var schemaDriverSequence atomic.Uint64

type schemaRecordingDriver struct {
	mu      sync.Mutex
	queries []string
	failAt  int
	failErr error
}

func (d *schemaRecordingDriver) Open(string) (driver.Conn, error) {
	return &schemaRecordingConn{driver: d}, nil
}

func (d *schemaRecordingDriver) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.queries...)
}

type schemaRecordingConn struct {
	driver *schemaRecordingDriver
}

func (c *schemaRecordingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (c *schemaRecordingConn) Close() error {
	return nil
}

func (c *schemaRecordingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c *schemaRecordingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()

	c.driver.queries = append(c.driver.queries, query)
	if c.driver.failAt > 0 && len(c.driver.queries) == c.driver.failAt {
		return nil, c.driver.failErr
	}
	return driver.RowsAffected(0), nil
}

func openSchemaRecordingDB(t *testing.T, recorder *schemaRecordingDriver) *sql.DB {
	t.Helper()
	driverName := fmt.Sprintf("stressbot_schema_test_%d", schemaDriverSequence.Add(1))
	sql.Register(driverName, recorder)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestInitMySQLSchemaOnlyCreatesTables(t *testing.T) {
	recorder := &schemaRecordingDriver{}
	db := openSchemaRecordingDB(t, recorder)

	if err := initMySQLSchema(db); err != nil {
		t.Fatalf("initMySQLSchema() error = %v", err)
	}

	queries := recorder.snapshot()
	if len(queries) != len(allDDL) {
		t.Fatalf("executed %d statements, want %d CREATE TABLE statements", len(queries), len(allDDL))
	}
	for i, query := range queries {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "CREATE TABLE") {
			t.Errorf("statement %d is not CREATE TABLE: %q", i, query)
		}
	}
}

func TestInitMySQLSchemaReturnsCreateTableError(t *testing.T) {
	sentinel := errors.New("create table failed")
	recorder := &schemaRecordingDriver{failAt: 2, failErr: sentinel}
	db := openSchemaRecordingDB(t, recorder)

	err := initMySQLSchema(db)
	if !errors.Is(err, sentinel) {
		t.Fatalf("initMySQLSchema() error = %v, want %v", err, sentinel)
	}
	if got := len(recorder.snapshot()); got != 2 {
		t.Fatalf("executed %d statements after failure, want 2", got)
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
