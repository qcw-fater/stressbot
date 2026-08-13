package config

import (
	"os"
	"strings"
	"testing"
)

func TestExpandString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{
			name:  "无$符号原样返回",
			input: "127.0.0.1",
			want:  "127.0.0.1",
		},
		{
			name:  "简单展开",
			input: "${TEST_HOST}",
			env:   map[string]string{"TEST_HOST": "192.168.1.1"},
			want:  "192.168.1.1",
		},
		{
			name:  "嵌入展开",
			input: "prefix-${TEST_HOST}-suffix",
			env:   map[string]string{"TEST_HOST": "val"},
			want:  "prefix-val-suffix",
		},
		{
			name:  "default展开_变量已定义",
			input: "${TEST_HOST:-fallback}",
			env:   map[string]string{"TEST_HOST": "real"},
			want:  "real",
		},
		{
			name:  "default展开_变量未定义",
			input: "${MISSING_VAR:-fallback}",
			want:  "fallback",
		},
		{
			name:  "default展开_变量定义为空",
			input: "${TEST_EMPTY:-fallback}",
			env:   map[string]string{"TEST_EMPTY": ""},
			want:  "fallback",
		},
		{
			name:    "无default_未定义报错",
			input:   "${TOTALLY_MISSING}",
			wantErr: true,
		},
		{
			name:  "美元符号转义",
			input: "$$abc",
			want:  "$abc",
		},
		{
			name:  "混合多种语法",
			input: "${TEST_HOST}:${TEST_PORT:-3306}",
			env:   map[string]string{"TEST_HOST": "db.local"},
			want:  "db.local:3306",
		},
		{
			name:  "空字符串引用",
			input: "${TEST_EMPTY:-}",
			env:   map[string]string{"TEST_EMPTY": ""},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 设置环境变量
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := expandString(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("expandString(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("expandString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExpandConfigStrings(t *testing.T) {
	t.Setenv("TEST_DB_PASS", "secret123")
	t.Setenv("TEST_DB_HOST", "10.0.0.1")

	type Nested struct {
		Password string `toml:"password"`
	}
	type Config struct {
		Host     string            `toml:"host"`
		Password string            `toml:"password"`
		Port     int               `toml:"port"` // 非字符串，不展开
		Nested   Nested            `toml:"nested"`
		Extra    map[string]string `toml:"extra"`
		Plain    string            `toml:"plain"`
	}

	cfg := &Config{
		Host:     "${TEST_DB_HOST}",
		Password: "${TEST_DB_PASS}",
		Port:     3306,
		Nested:   Nested{Password: "${TEST_DB_PASS}"},
		Extra:    map[string]string{"key": "${TEST_DB_HOST}"},
		Plain:    "no-env-here",
	}

	if err := ExpandConfigStrings(cfg); err != nil {
		t.Fatalf("ExpandConfigStrings failed: %v", err)
	}

	if cfg.Host != "10.0.0.1" {
		t.Errorf("Host = %q, want 10.0.0.1", cfg.Host)
	}
	if cfg.Password != "secret123" {
		t.Errorf("Password = %q, want secret123", cfg.Password)
	}
	if cfg.Port != 3306 {
		t.Errorf("Port = %d, want 3306 (不应被展开)", cfg.Port)
	}
	if cfg.Nested.Password != "secret123" {
		t.Errorf("Nested.Password = %q, want secret123", cfg.Nested.Password)
	}
	if cfg.Extra["key"] != "10.0.0.1" {
		t.Errorf("Extra[key] = %q, want 10.0.0.1", cfg.Extra["key"])
	}
	if cfg.Plain != "no-env-here" {
		t.Errorf("Plain = %q, want no-env-here", cfg.Plain)
	}
}

func TestExpandConfigStrings_UndefinedError(t *testing.T) {
	type Config struct {
		Password string `toml:"password"`
	}
	cfg := &Config{Password: "${UNDEFINED_PASSWORD}"}

	err := ExpandConfigStrings(cfg)
	if err == nil {
		t.Error("期望未定义环境变量返回 error，得到 nil")
	}
}

func TestLoadTOML(t *testing.T) {
	t.Setenv("TEST_TOML_HOST", "env-host-value")

	type Sub struct {
		Port int `toml:"port"`
	}
	type TestConfig struct {
		Name   string `toml:"name"`
		Host   string `toml:"host"`
		Sub    Sub    `toml:"sub"`
		Daemon bool   `toml:"daemon"`
	}

	// 写入临时 TOML 文件
	dir := t.TempDir()
	path := dir + "/test.toml"
	content := `
name = "test"
host = "${TEST_TOML_HOST}"
daemon = true

[sub]
port = 9090
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	defaults := TestConfig{Name: "default", Host: "default-host", Daemon: false}
	cfg, err := LoadTOML(path, defaults)
	if err != nil {
		t.Fatalf("LoadTOML failed: %v", err)
	}

	if cfg.Name != "test" {
		t.Errorf("Name = %q, want test", cfg.Name)
	}
	if cfg.Host != "env-host-value" {
		t.Errorf("Host = %q, want env-host-value (环境变量展开)", cfg.Host)
	}
	if cfg.Daemon != true {
		t.Errorf("Daemon = %v, want true", cfg.Daemon)
	}
	if cfg.Sub.Port != 9090 {
		t.Errorf("Sub.Port = %d, want 9090", cfg.Sub.Port)
	}
}

func TestLoadTOML_UnknownFieldError(t *testing.T) {
	type TestConfig struct {
		Name string `toml:"name"`
	}

	dir := t.TempDir()
	path := dir + "/test.toml"
	// misspelled 字段不在 struct 中
	content := `name = "ok"` + "\n" + `misspelled = "oops"` + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadTOML(path, TestConfig{})
	if err == nil {
		t.Fatal("期望未知字段返回 error，得到 nil")
	}
	if !strings.Contains(err.Error(), "未知字段") {
		t.Errorf("错误消息应包含「未知字段」，得到: %v", err)
	}
}
