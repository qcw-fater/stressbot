package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stressbot/config"

	mysqldriver "github.com/go-sql-driver/mysql"
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

// 密码里的 `@` `/` `:` `?` 都是 DSN 分隔符，手工拼接会让驱动把密码的后半段
// 当成主机名。这里断言 DSN 能被驱动原样解析回来。
func TestDSNEscapesSeparatorsInCredentials(t *testing.T) {
	cfg := MySQLConfig{
		Host:     "db.internal",
		Port:     3307,
		Username: "stress@svc",
		Password: "p@ss:w/ord?x#1",
		Database: "stress_history",
		Pool: config.ConnPoolConfig{
			DialTimeout:  "5s",
			ReadTimeout:  "30s",
			WriteTimeout: "45s",
		},
	}

	dsn, err := cfg.DSN()
	if err != nil {
		t.Fatalf("DSN() 返回错误: %v", err)
	}
	parsed, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("驱动无法解析 DSN %q: %v", dsn, err)
	}

	if parsed.User != cfg.Username {
		t.Errorf("用户名 = %q，期望 %q", parsed.User, cfg.Username)
	}
	if parsed.Passwd != cfg.Password {
		t.Errorf("密码 = %q，期望 %q", parsed.Passwd, cfg.Password)
	}
	if parsed.Addr != "db.internal:3307" {
		t.Errorf("地址 = %q，期望 db.internal:3307", parsed.Addr)
	}
	if parsed.DBName != cfg.Database {
		t.Errorf("库名 = %q，期望 %q", parsed.DBName, cfg.Database)
	}
	if !parsed.ParseTime {
		t.Error("parseTime 应为 true")
	}
	if parsed.Loc != time.Local {
		t.Errorf("loc = %v，期望 Local", parsed.Loc)
	}
	if parsed.Timeout != 5*time.Second || parsed.ReadTimeout != 30*time.Second || parsed.WriteTimeout != 45*time.Second {
		t.Errorf("超时参数 = %v/%v/%v，期望 5s/30s/45s", parsed.Timeout, parsed.ReadTimeout, parsed.WriteTimeout)
	}
}

// 端口留空回落 3306；未配的 timeout 不写进 DSN，交给驱动默认。
func TestDSNDefaultsPortAndOmitsUnsetTimeouts(t *testing.T) {
	cfg := MySQLConfig{Host: "127.0.0.1", Username: "root", Database: "stress"}

	dsn, err := cfg.DSN()
	if err != nil {
		t.Fatalf("DSN() 返回错误: %v", err)
	}
	parsed, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("驱动无法解析 DSN %q: %v", dsn, err)
	}
	if parsed.Addr != "127.0.0.1:3306" {
		t.Errorf("地址 = %q，期望 127.0.0.1:3306", parsed.Addr)
	}
	if parsed.Timeout != 0 || parsed.ReadTimeout != 0 || parsed.WriteTimeout != 0 {
		t.Errorf("未配的超时不应写入 DSN，实际 %v/%v/%v", parsed.Timeout, parsed.ReadTimeout, parsed.WriteTimeout)
	}
}

// 时长写错必须报错，不能静默退化成驱动默认。
func TestInvalidPoolDurationFailsLoud(t *testing.T) {
	cases := []struct {
		name string
		pool config.ConnPoolConfig
		want string
	}{
		{"拨号超时", config.ConnPoolConfig{DialTimeout: "5秒"}, "mysql.pool.dialTimeout"},
		{"读超时", config.ConnPoolConfig{ReadTimeout: "30"}, "mysql.pool.readTimeout"},
		{"写超时", config.ConnPoolConfig{WriteTimeout: "-"}, "mysql.pool.writeTimeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := MySQLConfig{Host: "127.0.0.1", Pool: tc.pool}
			_, err := cfg.DSN()
			if err == nil {
				t.Fatal("非法时长应返回错误")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息 = %q，应包含字段名 %s", err.Error(), tc.want)
			}
		})
	}

	cfg := MySQLConfig{Host: "127.0.0.1", Pool: config.ConnPoolConfig{ConnMaxLifetime: "1小时"}}
	if _, err := cfg.ConnMaxLifetime(); err == nil {
		t.Fatal("非法 connMaxLifetime 应返回错误")
	}
}

// 非法的连接池时长必须让 LoadConfig 整体失败，而不是等到装配阶段。
func TestLoadConfigRejectsInvalidPoolDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.toml")
	content := "[mysql]\nhost = \"127.0.0.1\"\n\n[mysql.pool]\ndialTimeout = \"5秒\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("非法 mysql.pool.dialTimeout 应让配置加载失败")
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
