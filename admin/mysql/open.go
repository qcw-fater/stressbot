package mysql

import (
	"context"
	"database/sql"
	"time"

	// Register the MySQL database/sql driver used by Open.
	_ "github.com/go-sql-driver/mysql"
)

// PoolConfig contains the database/sql pool limits used by Admin.
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime string
}

// Open creates and verifies the Admin MySQL connection pool.
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
	if lifetime, err := time.ParseDuration(pool.ConnMaxLifetime); err == nil && lifetime > 0 {
		db.SetConnMaxLifetime(lifetime)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
