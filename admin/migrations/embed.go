package migrations

import (
	"embed"

	"github.com/pressly/goose/v3"
)

// Files 是编译进 Admin 二进制的 SQL migration 集合。
//
//go:embed *.sql
var Files embed.FS

// GoMigrations 显式返回本项目的 Go migration，禁止依赖 Goose 全局注册表。
func GoMigrations() []*goose.Migration {
	return []*goose.Migration{
		reconcileHistoryMigration(),
		reconcileTemplatesMigration(),
	}
}
