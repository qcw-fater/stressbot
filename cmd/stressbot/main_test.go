package main

import (
	"path/filepath"
	"testing"
)

func TestParseOptions(t *testing.T) {
	defaults, err := parseOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.configPath != "conf/stressbot.toml" || defaults.flowPath != "" || defaults.daemon {
		t.Fatalf("默认参数错误: %+v", defaults)
	}

	overrides, err := parseOptions([]string{
		"-config", "custom.toml",
		"-flow", "flow.json",
		"-proto", "proto",
		"-scripts", "scripts",
		"-adapter", "adapter",
		"-d",
	})
	if err != nil {
		t.Fatal(err)
	}
	if overrides.configPath != "custom.toml" || overrides.flowPath != "flow.json" ||
		overrides.protoDir != "proto" || overrides.scriptsDir != "scripts" ||
		overrides.adapterDir != "adapter" || !overrides.daemon {
		t.Fatalf("覆盖参数错误: %+v", overrides)
	}
}

func TestRunReturnsConfigError(t *testing.T) {
	if code := run([]string{"-config", filepath.Join(t.TempDir(), "missing.toml")}); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}
