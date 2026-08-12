package main

import (
	"path/filepath"
	"testing"
)

// absOrFatal 在测试中复刻 resolvePath 的绝对路径解析，保证跨平台断言一致。
func absOrFatal(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("filepath.Abs(%q) 失败: %v", p, err)
	}
	return a
}

func TestResolveStandalonePaths_DefaultsWhenEmpty(t *testing.T) {
	const confDir = "/opt/stressbot/conf"
	got := resolveStandalonePaths(confDir, "", "", "", "")

	want := standalonePaths{
		Flow:    filepath.Join(confDir, "flow", "flow.json"),
		Proto:   filepath.Join(confDir, "proto"),
		Scripts: filepath.Join(confDir, "scripts"),
		Adapter: filepath.Join(confDir, "adapter"),
	}
	if got != want {
		t.Fatalf("空 flag 应回退 confDir 默认: got=%+v want=%+v", got, want)
	}
}

func TestResolveStandalonePaths_OverrideWhenProvided(t *testing.T) {
	const confDir = "/opt/stressbot/conf"
	in := standalonePaths{
		Flow:    "/data/flow/rank.json",
		Proto:   "/data/proto",
		Scripts: "/data/scripts",
		Adapter: "/data/adapter",
	}
	got := resolveStandalonePaths(confDir, in.Flow, in.Proto, in.Scripts, in.Adapter)

	want := standalonePaths{
		Flow:    absOrFatal(t, in.Flow),
		Proto:   absOrFatal(t, in.Proto),
		Scripts: absOrFatal(t, in.Scripts),
		Adapter: absOrFatal(t, in.Adapter),
	}
	if got != want {
		t.Fatalf("非空 flag 应解析为绝对路径: got=%+v want=%+v", got, want)
	}
}

func TestResolveStandalonePaths_PartialOverride(t *testing.T) {
	const confDir = "/opt/stressbot/conf"
	got := resolveStandalonePaths(confDir, "/data/flow/rank.json", "", "", "")

	if got.Flow != absOrFatal(t, "/data/flow/rank.json") {
		t.Errorf("flow 覆盖失败: got=%q", got.Flow)
	}
	if got.Proto != filepath.Join(confDir, "proto") {
		t.Errorf("proto 空 flag 应回退默认: got=%q", got.Proto)
	}
	if got.Scripts != filepath.Join(confDir, "scripts") {
		t.Errorf("scripts 空 flag 应回退默认: got=%q", got.Scripts)
	}
	if got.Adapter != filepath.Join(confDir, "adapter") {
		t.Errorf("adapter 空 flag 应回退默认: got=%q", got.Adapter)
	}
}
