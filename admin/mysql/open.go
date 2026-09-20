// Package mysql 提供 Admin 的 MySQL 连接池初始化与数据库结构（内置 DDL）管理。
package mysql

import (
	"context"
	"database/sql"
	"time"

	// 注册 Open 使用的 MySQL database/sql 驱动。
	_ "github.com/go-sql-driver/mysql"
)

// PoolConfig 是 Admin 使用的 database/sql 连接池上限。
// ConnMaxLifetime 取已解析的时长：配置字符串的合法性由调用方在加载期校验，
// 这里再解析一次就只能吞掉错误或重复报错。
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Open 创建并验证 Admin 的 MySQL 连接池。
func Open(dsn string, pool PoolConfig) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	if pool.MaxOpenConns > 0 {
		db.SetMaxOpenConns(pool.MaxOpenConns)
	}
	if pool.MaxIdleConns > 0 {
		db.SetMaxIdleConns(pool.MaxIdleConns)
	}
	if pool.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(pool.ConnMaxLifetime)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
