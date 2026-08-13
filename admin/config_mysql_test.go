package admin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMySQLEnabledRequiresHost(t *testing.T) {
	cfg := Defaults()
	if cfg.MySQLEnabled() {
		t.Fatal("默认空 host 不应启用 MySQL")
	}
	cfg.MySQL.Host = "127.0.0.1"
	if !cfg.MySQLEnabled() {
		t.Fatal("非空 host 应启用 MySQL")
	}
	cfg.MySQL.Host = "   "
	if cfg.MySQLEnabled() {
		t.Fatal("纯空白 host 不应启用 MySQL")
	}
}

func TestLoadConfigWithoutMySQLDisablesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.toml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MySQLEnabled() {
		t.Fatal("省略 [mysql] 时不应启用 MySQL")
	}
}

func TestLoadConfigWithEmptyMySQLHostDisablesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.toml")
	if err := os.WriteFile(path, []byte("[mysql]\nhost = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MySQLEnabled() {
		t.Fatal("mysql.host 为空时不应启用 MySQL")
	}
}
