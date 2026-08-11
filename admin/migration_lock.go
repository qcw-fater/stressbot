package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const migrationLockName = "stressbot_schema_migration"

var ErrMigrationLockTimeout = errors.New("等待数据库迁移锁超时")

type migrationLock struct {
	conn *sql.Conn
	once sync.Once
	err  error
}

func acquireMigrationLock(ctx context.Context, db *sql.DB, timeout time.Duration) (*migrationLock, error) {
	if db == nil {
		return nil, errors.New("数据库连接不能为空")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	seconds := int64(math.Ceil(timeout.Seconds()))
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取迁移专用数据库会话: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = conn.Close()
		}
	}()

	var status sql.NullInt64
	if err := conn.QueryRowContext(ctx,
		"SELECT GET_LOCK('"+migrationLockName+"', ?)", seconds,
	).Scan(&status); err != nil {
		return nil, fmt.Errorf("获取数据库迁移锁: %w", err)
	}
	if !status.Valid {
		return nil, errors.New("获取数据库迁移锁返回 NULL")
	}
	if status.Int64 != 1 {
		if status.Int64 == 0 {
			return nil, ErrMigrationLockTimeout
		}
		return nil, fmt.Errorf("获取数据库迁移锁返回非法状态 %d", status.Int64)
	}

	closeOnError = false
	return &migrationLock{conn: conn}, nil
}

// Release 使用独立清理上下文，确保业务 context 取消后仍尝试在同一 MySQL session 释放锁。
func (l *migrationLock) Release() error {
	if l == nil || l.conn == nil {
		return nil
	}
	l.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var status sql.NullInt64
		queryErr := l.conn.QueryRowContext(ctx,
			"SELECT RELEASE_LOCK('"+migrationLockName+"')",
		).Scan(&status)
		if queryErr != nil {
			queryErr = fmt.Errorf("释放数据库迁移锁: %w", queryErr)
		} else if !status.Valid {
			queryErr = errors.New("释放数据库迁移锁返回 NULL")
		} else if status.Int64 != 1 {
			queryErr = fmt.Errorf("释放数据库迁移锁返回状态 %d", status.Int64)
		}
		l.err = errors.Join(queryErr, l.conn.Close())
	})
	return l.err
}

func withMigrationLock(
	ctx context.Context,
	db *sql.DB,
	timeout time.Duration,
	fn func(context.Context) error,
) (err error) {
	lock, err := acquireMigrationLock(ctx, db, timeout)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, lock.Release())
	}()
	return fn(ctx)
}
