package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"stressbot/admin/migrations"
	stresslog "stressbot/utils/log"

	"github.com/pressly/goose/v3"
	"go.uber.org/zap"
)

type MigrationCommand string

const (
	MigrationAuto    MigrationCommand = "auto"
	MigrationStatus  MigrationCommand = "status"
	MigrationUp      MigrationCommand = "up"
	MigrationUpByOne MigrationCommand = "up-by-one"
)

type migrationProvider interface {
	Up(context.Context) ([]*goose.MigrationResult, error)
	UpByOne(context.Context) (*goose.MigrationResult, error)
	Status(context.Context) ([]*goose.MigrationStatus, error)
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

func ParseMigrationCommand(raw string) (MigrationCommand, error) {
	command := MigrationCommand(strings.ToLower(strings.TrimSpace(raw)))
	if command == "" {
		command = MigrationAuto
	}
	switch command {
	case MigrationAuto, MigrationStatus, MigrationUp, MigrationUpByOne:
		return command, nil
	default:
		return "", fmt.Errorf("不支持的数据库迁移命令 %q（仅支持 auto、status、up、up-by-one）", raw)
	}
}

func executeMigrationCommand(
	ctx context.Context,
	provider migrationProvider,
	command MigrationCommand,
	out io.Writer,
	postCheck func(context.Context) error,
) error {
	if out == nil {
		out = io.Discard
	}
	switch command {
	case MigrationAuto, MigrationUp:
		results, err := provider.Up(ctx)
		if err != nil {
			logPartialMigrationError(err)
			return fmt.Errorf("执行数据库前向迁移: %w", err)
		}
		for _, result := range results {
			writeMigrationResult(out, result)
			logMigrationResult(result, nil)
		}
		if postCheck != nil {
			if err := postCheck(ctx); err != nil {
				return err
			}
		}
		return nil
	case MigrationUpByOne:
		result, err := provider.UpByOne(ctx)
		if err != nil {
			if errors.Is(err, goose.ErrNoNextVersion) {
				_, _ = fmt.Fprintln(out, "没有待执行的数据库迁移")
				return nil
			}
			logPartialMigrationError(err)
			return fmt.Errorf("执行单步数据库前向迁移: %w", err)
		}
		if result == nil {
			_, _ = fmt.Fprintln(out, "没有待执行的数据库迁移")
			return nil
		}
		writeMigrationResult(out, result)
		logMigrationResult(result, nil)
		return nil
	case MigrationStatus:
		statuses, err := provider.Status(ctx)
		if err != nil {
			return fmt.Errorf("查询数据库迁移状态: %w", err)
		}
		for _, status := range statuses {
			appliedAt := "-"
			if !status.AppliedAt.IsZero() {
				appliedAt = status.AppliedAt.Format(time.RFC3339)
			}
			_, _ = fmt.Fprintf(out, "%05d\t%s\t%s\t%s\n",
				status.Source.Version,
				status.State,
				filepath.Base(status.Source.Path),
				appliedAt,
			)
		}
		return nil
	default:
		return fmt.Errorf("不支持的数据库迁移命令 %q", command)
	}
}

func writeMigrationResult(out io.Writer, result *goose.MigrationResult) {
	if result == nil || result.Source == nil {
		return
	}
	_, _ = fmt.Fprintf(out, "%05d\t%s\t%s\t%s\n",
		result.Source.Version,
		result.Direction,
		filepath.Base(result.Source.Path),
		result.Duration,
	)
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

func runMySQLMigrations(
	ctx context.Context,
	db *sql.DB,
	command MigrationCommand,
	out io.Writer,
) error {
	if db == nil {
		return errors.New("数据库连接不能为空")
	}
	if db.Stats().MaxOpenConnections == 1 {
		return errors.New("数据库迁移要求 mysql.maxOpenConns 为 0（不限制）或至少 2，以免 advisory lock 与 Goose 连接互相等待")
	}
	return withMigrationLock(ctx, db, 30*time.Second, func(ctx context.Context) error {
		provider, err := newMigrationProvider(goose.DialectMySQL, db, migrations.Files, migrations.GoMigrations())
		if err != nil {
			return fmt.Errorf("构造 Goose Provider: %w", err)
		}
		return executeMigrationCommand(ctx, provider, command, out, func(ctx context.Context) error {
			return postCheckMySQLSchema(ctx, db)
		})
	})
}

// RunMigrationCommand 供 cmd/admin 的只迁移模式使用；不会创建或启动任何 HTTP listener。
func RunMigrationCommand(ctx context.Context, cfg Config, command MigrationCommand, out io.Writer) error {
	if cfg.MySQL == nil {
		return errors.New("未配置 MySQL，无法执行数据库迁移命令")
	}
	db, err := openDB(*cfg.MySQL)
	if err != nil {
		return fmt.Errorf("连接 MySQL: %w", err)
	}
	defer db.Close()
	return runMySQLMigrations(ctx, db, command, out)
}
