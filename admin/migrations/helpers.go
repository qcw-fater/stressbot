package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

func currentDatabase(ctx context.Context, db *sql.DB) (string, error) {
	var name sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&name); err != nil {
		return "", fmt.Errorf("查询当前数据库: %w", err)
	}
	if !name.Valid || strings.TrimSpace(name.String) == "" {
		return "", errors.New("当前连接未选择数据库")
	}
	return name.String, nil
}

func columnExists(ctx context.Context, db *sql.DB, database, table, column string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND COLUMN_NAME = ?
		)`, database, table, column).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("检查列 %s.%s: %w", table, column, err)
	}
	return exists, nil
}

func addColumnIfMissing(
	ctx context.Context,
	db *sql.DB,
	database, table, column, ddl string,
) error {
	exists, err := columnExists(ctx, db, database, table, column)
	if err != nil || exists {
		return err
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("新增列 %s.%s: %w", table, column, err)
	}
	return nil
}

func indexExists(ctx context.Context, db *sql.DB, database, table, index string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND INDEX_NAME = ?
		)`, database, table, index).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("检查索引 %s.%s: %w", table, index, err)
	}
	return exists, nil
}

func addIndexIfMissing(
	ctx context.Context,
	db *sql.DB,
	database, table, index, ddl string,
) error {
	exists, err := indexExists(ctx, db, database, table, index)
	if err != nil || exists {
		return err
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("新增索引 %s.%s: %w", table, index, err)
	}
	return nil
}

func columnProperties(
	ctx context.Context,
	db *sql.DB,
	database, table, column string,
) (exists bool, nullable bool, collation string, err error) {
	var isNullable string
	var collationName sql.NullString
	err = db.QueryRowContext(ctx, `
		SELECT IS_NULLABLE, COLLATION_NAME
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		database, table, column,
	).Scan(&isNullable, &collationName)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, "", nil
	}
	if err != nil {
		return false, false, "", fmt.Errorf("读取列属性 %s.%s: %w", table, column, err)
	}
	return true, strings.EqualFold(isNullable, "YES"), collationName.String, nil
}

func alterColumnNullability(
	ctx context.Context,
	db *sql.DB,
	database, table, column string,
	wantNullable bool,
	ddl string,
) error {
	exists, nullable, _, err := columnProperties(ctx, db, database, table, column)
	if err != nil || !exists || nullable == wantNullable {
		return err
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("调整列空值约束 %s.%s: %w", table, column, err)
	}
	return nil
}

func ensureColumnCollation(
	ctx context.Context,
	db *sql.DB,
	database, table, column, wantCollation, ddl string,
) error {
	exists, _, collation, err := columnProperties(ctx, db, database, table, column)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("列 %s.%s 不存在", table, column)
	}
	if strings.EqualFold(collation, wantCollation) {
		return nil
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("调整列排序规则 %s.%s: %w", table, column, err)
	}
	return nil
}

func ensureUniqueIndex(
	ctx context.Context,
	db *sql.DB,
	database, table, index, columns, addDDL string,
) error {
	rows, err := db.QueryContext(ctx, `
		SELECT COLUMN_NAME, NON_UNIQUE
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND INDEX_NAME = ?
		ORDER BY SEQ_IN_INDEX`, database, table, index)
	if err != nil {
		return fmt.Errorf("读取唯一索引 %s.%s: %w", table, index, err)
	}
	var gotColumns []string
	unique := true
	for rows.Next() {
		var column string
		var nonUnique int
		if err := rows.Scan(&column, &nonUnique); err != nil {
			_ = rows.Close()
			return fmt.Errorf("读取唯一索引 %s.%s: %w", table, index, err)
		}
		gotColumns = append(gotColumns, column)
		unique = unique && nonUnique == 0
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("遍历唯一索引 %s.%s: %w", table, index, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭唯一索引结果集 %s.%s: %w", table, index, err)
	}
	wantColumns := strings.Split(columns, ",")
	for i := range wantColumns {
		wantColumns[i] = strings.TrimSpace(wantColumns[i])
	}
	if unique && slices.Equal(gotColumns, wantColumns) {
		return nil
	}
	if len(gotColumns) > 0 {
		if _, err := db.ExecContext(ctx, "ALTER TABLE "+table+" DROP INDEX "+index); err != nil {
			return fmt.Errorf("删除错误索引 %s.%s: %w", table, index, err)
		}
	}
	if _, err := db.ExecContext(ctx, addDDL); err != nil {
		return fmt.Errorf("新增唯一索引 %s.%s: %w", table, index, err)
	}
	return nil
}

func ensurePrimaryKey(
	ctx context.Context,
	db *sql.DB,
	database, table string,
	wantColumns []string,
	addDDL string,
) error {
	rows, err := db.QueryContext(ctx, `
		SELECT COLUMN_NAME
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND INDEX_NAME = 'PRIMARY'
		ORDER BY SEQ_IN_INDEX`, database, table)
	if err != nil {
		return fmt.Errorf("读取主键 %s: %w", table, err)
	}
	var got []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			_ = rows.Close()
			return fmt.Errorf("读取主键 %s: %w", table, err)
		}
		got = append(got, column)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("遍历主键 %s: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭主键结果集 %s: %w", table, err)
	}
	if slices.Equal(got, wantColumns) {
		return nil
	}
	ddl := addDDL
	if len(got) > 0 {
		ddl = "ALTER TABLE " + table + " DROP PRIMARY KEY, " + strings.TrimPrefix(addDDL, "ALTER TABLE "+table+" ")
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("调整主键 %s: %w", table, err)
	}
	return nil
}
