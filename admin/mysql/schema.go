package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// InitializeSchema creates the tables required by the first Admin release.
// The schema is deliberately unversioned: this release only supports a new database.
func InitializeSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("数据库连接不能为空")
	}
	for _, statement := range currentSchema() {
		if _, err := db.ExecContext(ctx, statement.ddl); err != nil {
			return fmt.Errorf("初始化数据库表 %s: %w", statement.table, err)
		}
	}
	return nil
}
