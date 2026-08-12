package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"stressbot/admin/migrations"
	stresslog "stressbot/utils/log"

	"github.com/pressly/goose/v3"
	"go.uber.org/zap"
)

type migrationProvider interface {
	Up(context.Context) ([]*goose.MigrationResult, error)
}

// newMigrationProvider 构造完全实例化的 Goose Provider。
// 禁用全局 Go migration registry，避免测试或其他包的 init 注册污染本进程的迁移集合。
func newMigrationProvider(
	dialect goose.Dialect,
	db *sql.DB,
	files fs.FS,
	goMigrations []*goose.Migration,
) (*goose.Provider, error) {
	opts := []goose.ProviderOption{
		goose.WithDisableGlobalRegistry(true),
	}
	if len(goMigrations) > 0 {
		opts = append(opts, goose.WithGoMigrations(goMigrations...))
	}
	return goose.NewProvider(dialect, db, files, opts...)
}

func executeMigrations(
	ctx context.Context,
	provider migrationProvider,
	postCheck func(context.Context) error,
) ([]*goose.MigrationResult, error) {
	results, err := provider.Up(ctx)
	if err != nil {
		logPartialMigrationError(err)
		return nil, fmt.Errorf("执行数据库前向迁移: %w", err)
	}
	for _, result := range results {
		logMigrationResult(result, nil)
	}
	if postCheck != nil {
		if err := postCheck(ctx); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func logMigrationResult(result *goose.MigrationResult, migrationErr error) {
	logger := stresslog.GetLogger()
	if logger == nil || result == nil || result.Source == nil {
		return
	}
	fields := []zap.Field{
		zap.Int64("version", result.Source.Version),
		zap.String("source", filepath.Base(result.Source.Path)),
		zap.Duration("duration", result.Duration),
		zap.String("direction", result.Direction),
	}
	if migrationErr != nil {
		logger.Error("[MIGRATION] 数据库迁移失败", append(fields, zap.Error(migrationErr))...)
		return
	}
	logger.Info("[MIGRATION] 数据库迁移完成", fields...)
}

func logPartialMigrationError(err error) {
	var partial *goose.PartialError
	if !errors.As(err, &partial) {
		return
	}
	for _, result := range partial.Applied {
		logMigrationResult(result, nil)
	}
	if partial.Failed != nil {
		logMigrationResult(partial.Failed, partial.Failed.Error)
	}
}

func runMySQLMigrations(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("数据库连接不能为空")
	}
	if db.Stats().MaxOpenConnections == 1 {
		return errors.New("数据库迁移要求 mysql.maxOpenConns 为 0（不限制）或至少 2，以免 advisory lock 与 Goose 连接互相等待")
	}
	startedAt := time.Now()
	appliedCount := 0
	err := withMigrationLock(ctx, db, 30*time.Second, func(ctx context.Context) error {
		provider, err := newMigrationProvider(goose.DialectMySQL, db, migrations.Files, migrations.GoMigrations())
		if err != nil {
			return fmt.Errorf("构造 Goose Provider: %w", err)
		}
		results, err := executeMigrations(ctx, provider, func(ctx context.Context) error {
			return postCheckMySQLSchema(ctx, db)
		})
		appliedCount = len(results)
		return err
	})
	if err != nil {
		return err
	}
	stresslog.Info(migrationSummaryMessage(appliedCount),
		zap.Int("applied", appliedCount),
		zap.Duration("duration", time.Since(startedAt)))
	return nil
}

func migrationSummaryMessage(appliedCount int) string {
	if appliedCount == 0 {
		return "[MIGRATION] 数据库结构已是最新版本"
	}
	return fmt.Sprintf("[MIGRATION] 数据库迁移完成，共执行 %d 个版本", appliedCount)
}
