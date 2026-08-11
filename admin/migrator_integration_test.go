package admin

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"stressbot/admin/migrations"

	"github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"
)

func integrationMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("STRESSBOT_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未设置 STRESSBOT_TEST_MYSQL_DSN，跳过 MySQL 迁移集成测试")
	}
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("STRESSBOT_TEST_MYSQL_DSN 格式无效（值已隐藏）")
	}
	if !strings.Contains(strings.ToLower(parsed.DBName), "test") {
		t.Fatalf("迁移集成测试只允许使用库名含 test 的专用数据库，当前库名为 %q", parsed.DBName)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("打开测试数据库失败（DSN 已隐藏）: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("连接测试数据库失败（DSN 已隐藏）: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func resetMigrationIntegrationSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("disable foreign key checks: %v", err)
	}
	defer db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1")
	for _, table := range []string{
		"goose_db_version", "task_assignment", "task_report", "task_aggregated", "task_timeseries",
		"task_config_archive", "task_agent_events", "task_meta", "flow_template", "action_template",
		"listen_template", "task_history", "migration_failure_probe",
	} {
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatalf("drop integration table %s: %v", table, err)
		}
	}
}

func createEarlyHistorySchema(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE task_history (
			id VARCHAR(32) NOT NULL PRIMARY KEY, name VARCHAR(255) NOT NULL DEFAULT '',
			state VARCHAR(32) NOT NULL DEFAULT '', total_bots INT NOT NULL DEFAULT 0,
			agent_count INT NOT NULL DEFAULT 0, created_at DATETIME(3) NOT NULL,
			started_at DATETIME(3) NULL, stopped_at DATETIME(3) NULL,
			duration_sec INT NOT NULL DEFAULT 0, error_msg TEXT,
			starred TINYINT(1) NOT NULL DEFAULT 0, tags JSON NULL, note TEXT,
			config_summary JSON NULL, INDEX idx_state (state), INDEX idx_created (created_at DESC)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE task_assignment (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, task_id VARCHAR(32) NOT NULL,
			agent_id VARCHAR(64) NOT NULL, start_number INT NOT NULL DEFAULT 0,
			total_bots INT NOT NULL DEFAULT 0, INDEX idx_task (task_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE task_report (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, task_id VARCHAR(32) NOT NULL,
			agent_id VARCHAR(64) NOT NULL, agent_name VARCHAR(255) NOT NULL DEFAULT '',
			result VARCHAR(32) NOT NULL DEFAULT '', error_msg TEXT, finished_at DATETIME(3) NULL,
			final_snapshot JSON NULL, INDEX idx_task (task_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE task_aggregated (
			task_id VARCHAR(32) NOT NULL PRIMARY KEY, final_stress JSON NULL, final_system JSON NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE task_timeseries (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, task_id VARCHAR(32) NOT NULL,
			sampled_at DATETIME(3) NOT NULL, elapsed_sec INT NOT NULL DEFAULT 0,
			data_type VARCHAR(32) NOT NULL, snapshot JSON NOT NULL,
			INDEX idx_task_type (task_id, data_type, elapsed_sec)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE task_config_archive (
			task_id VARCHAR(32) NOT NULL PRIMARY KEY, flow_json MEDIUMBLOB NULL,
			proto_files MEDIUMBLOB NULL, lua_scripts MEDIUMBLOB NULL, robot_config JSON NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`INSERT INTO task_history
			(id, name, state, created_at, starred, tags, note)
			VALUES ('legacy-task', '旧任务', 'stopped', UTC_TIMESTAMP(3), 1, JSON_ARRAY('legacy'), '保留备注')`,
		`INSERT INTO task_timeseries
			(task_id, sampled_at, elapsed_sec, data_type, snapshot)
			VALUES ('legacy-task', UTC_TIMESTAMP(3), 1, 'stress', JSON_OBJECT('qps', 1))`,
	}
	for i, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("create early history schema statement %d: %v", i+1, err)
		}
	}
}

func createLegacyTemplateSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for i, statement := range []string{
		`CREATE TABLE action_template (
			id VARCHAR(32) NOT NULL PRIMARY KEY, name VARCHAR(80) NOT NULL,
			description VARCHAR(500) NULL, pattern VARCHAR(32) NOT NULL, data_json MEDIUMBLOB NOT NULL,
			created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
			INDEX idx_action_template_updated (updated_at DESC)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci`,
		`CREATE TABLE listen_template (
			id VARCHAR(32) NOT NULL PRIMARY KEY, name VARCHAR(80) NOT NULL,
			description VARCHAR(500) NULL, kind VARCHAR(32) NOT NULL, data_json MEDIUMBLOB NOT NULL,
			default_ref_json MEDIUMBLOB NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
			INDEX idx_listen_template_updated (updated_at DESC)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci`,
	} {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("create legacy template schema statement %d: %v", i+1, err)
		}
	}
}

func assertMigrationsIdempotent(t *testing.T, db *sql.DB) {
	t.Helper()
	for attempt := 1; attempt <= 2; attempt++ {
		if err := runMySQLMigrations(context.Background(), db, MigrationUp, io.Discard); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt, err)
		}
	}
}

func TestMigrationIntegration(t *testing.T) {
	db := integrationMigrationDB(t)

	t.Run("empty database", func(t *testing.T) {
		resetMigrationIntegrationSchema(t, db)
		assertMigrationsIdempotent(t, db)
	})

	t.Run("early history schema", func(t *testing.T) {
		resetMigrationIntegrationSchema(t, db)
		createEarlyHistorySchema(t, db)
		assertMigrationsIdempotent(t, db)
		var starred int
		var note string
		if err := db.QueryRow(`SELECT starred, note FROM task_meta WHERE task_id='legacy-task' AND stage_index=-1`).Scan(&starred, &note); err != nil {
			t.Fatalf("read migrated legacy metadata: %v", err)
		}
		if starred != 1 || note != "保留备注" {
			t.Fatalf("migrated metadata = (%d, %q), want (1, 保留备注)", starred, note)
		}
	})

	t.Run("legacy template indexes", func(t *testing.T) {
		resetMigrationIntegrationSchema(t, db)
		createLegacyTemplateSchema(t, db)
		assertMigrationsIdempotent(t, db)
	})

	t.Run("failure resumes forward", func(t *testing.T) {
		resetMigrationIntegrationSchema(t, db)
		createEarlyHistorySchema(t, db)
		sentinel := errors.New("injected migration failure")
		failure := goose.NewGoMigration(2, &goose.GoFunc{RunDB: func(ctx context.Context, db *sql.DB) error {
			_, err := db.ExecContext(ctx, "ALTER TABLE task_history ADD COLUMN active_agent_count INT NOT NULL DEFAULT 0 AFTER agent_count")
			if err != nil {
				return err
			}
			return sentinel
		}}, nil)
		failure.Source = "00002_injected_failure.go"
		provider, err := newMigrationProvider(goose.DialectMySQL, db, migrations.Files, []*goose.Migration{failure})
		if err != nil {
			t.Fatalf("create failure provider: %v", err)
		}
		_, err = provider.Up(context.Background())
		if !errors.Is(err, sentinel) {
			t.Fatalf("injected migration error = %v, want sentinel", err)
		}
		var applied int
		if err := db.QueryRow(`SELECT COUNT(*) FROM goose_db_version WHERE version_id=2 AND is_applied=1`).Scan(&applied); err != nil {
			t.Fatalf("read failed migration version: %v", err)
		}
		if applied != 0 {
			t.Fatalf("failed migration version marked applied: %d", applied)
		}
		if err := postCheckMySQLSchema(context.Background(), db); err == nil {
			t.Fatal("post-check accepted partially migrated schema")
		}
		assertMigrationsIdempotent(t, db)
	})

	t.Run("concurrent admins serialize", func(t *testing.T) {
		resetMigrationIntegrationSchema(t, db)
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- runMySQLMigrations(context.Background(), db, MigrationUp, io.Discard)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent migration: %v", err)
			}
		}
	})
}
