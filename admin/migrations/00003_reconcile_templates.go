package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func reconcileTemplatesMigration() *goose.Migration {
	migration := goose.NewGoMigration(3, &goose.GoFunc{RunDB: reconcileTemplates}, nil)
	migration.Source = "00003_reconcile_templates.go"
	return migration
}

func reconcileTemplates(ctx context.Context, db *sql.DB) error {
	database, err := currentDatabase(ctx, db)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		table string
		index string
	}{
		{"action_template", "uq_action_template_name"},
		{"listen_template", "uq_listen_template_name"},
	} {
		if err := ensureColumnCollation(ctx, db, database, item.table, "name", "utf8mb4_bin",
			"ALTER TABLE "+item.table+" MODIFY COLUMN name VARCHAR(80) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL"); err != nil {
			return err
		}
		if err := ensureUniqueIndex(ctx, db, database, item.table, item.index, "name",
			"ALTER TABLE "+item.table+" ADD UNIQUE INDEX "+item.index+" (name)"); err != nil {
			return err
		}
	}
	return nil
}
