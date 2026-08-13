package stresslog

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestFileLogUsesStableJSONSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stressbot.log")
	closeLog := InitLog(path, "agent", testLogConfig(), "")
	t.Cleanup(func() {
		if err := closeLog(); err != nil {
			t.Errorf("close log: %v", err)
		}
	})

	Info("agent connected", zap.String("agentId", "agent-1"))
	if err := GetLogger().Sync(); err != nil {
		t.Fatalf("sync log: %v", err)
	}

	line, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	var entry map[string]any
	if err := json.Unmarshal(line, &entry); err != nil {
		t.Fatalf("decode log line: %v\n%s", err, line)
	}
	for _, key := range []string{"timestamp", "level", "message", "caller", "service", "agentId"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("missing stable field %q in %v", key, entry)
		}
	}
	for _, key := range []string{"T", "L", "M", "C", "S", "SR"} {
		if _, ok := entry[key]; ok {
			t.Errorf("legacy short field %q remains in %v", key, entry)
		}
	}

	timestamp, ok := entry["timestamp"].(string)
	if !ok {
		t.Fatalf("timestamp is not a string: %T", entry["timestamp"])
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		t.Fatalf("timestamp is not RFC3339Nano: %q: %v", timestamp, err)
	}
	if parsed.Location() != time.UTC {
		t.Fatalf("timestamp is not UTC: %q", timestamp)
	}
}

func TestFileLogBuffersUntilSync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stressbot.log")
	closeLog := InitLog(path, "admin", testLogConfig(), "")
	t.Cleanup(func() {
		if err := closeLog(); err != nil {
			t.Errorf("close log: %v", err)
		}
	})

	Info("buffered message")
	before, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read log before sync: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("file sink wrote before sync: %q", before)
	}

	if err := GetLogger().Sync(); err != nil {
		t.Fatalf("sync log: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log after sync: %v", err)
	}
	if len(after) == 0 {
		t.Fatal("file sink did not flush on sync")
	}
}

func TestInitLogReturnsIdempotentCloser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stressbot.log")
	closeLog := InitLog(path, "admin", testLogConfig(), "")

	Info("flush on close")
	if err := closeLog(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || len(data) == 0 {
		t.Fatalf("close did not flush log: bytes=%d err=%v", len(data), err)
	}
}

func TestErrorAndDPanicLogsFlushImmediately(t *testing.T) {
	tests := []struct {
		name string
		log  func()
	}{
		{name: "Error", log: func() { Error("error message") }},
		{name: "ErrorS", log: func() { ErrorS("error message", "component", "test") }},
		{name: "ErrorF", log: func() { ErrorF("error %s", "message") }},
		{name: "DPanic", log: func() { DPanic("dpanic message") }},
		{name: "DPanicS", log: func() { DPanicS("dpanic message", "component", "test") }},
		{name: "DPanicF", log: func() { DPanicF("dpanic %s", "message") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "stressbot.log")
			closeLog := InitLog(path, "admin", testLogConfig(), "")
			t.Cleanup(func() {
				if err := closeLog(); err != nil {
					t.Errorf("close log: %v", err)
				}
			})

			tt.log()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read high-severity log: %v", err)
			}
			if len(data) == 0 {
				t.Fatal("high-severity log was not flushed immediately")
			}
		})
	}
}

func TestFatalLogsFlushBeforeExit(t *testing.T) {
	if os.Getenv("STRESSBOT_FATAL_LOG_HELPER") == "1" {
		InitLog(os.Getenv("STRESSBOT_FATAL_LOG_PATH"), "admin", testLogConfig(), "")
		switch os.Getenv("STRESSBOT_FATAL_LOG_KIND") {
		case "structured":
			Fatal("fatal structured message")
		case "formatted":
			FatalF("fatal %s message", "formatted")
		default:
			os.Exit(3)
		}
		return
	}

	for _, kind := range []string{"structured", "formatted"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "stressbot.log")
			cmd := exec.Command(os.Args[0], "-test.run=^TestFatalLogsFlushBeforeExit$")
			cmd.Env = append(os.Environ(),
				"STRESSBOT_FATAL_LOG_HELPER=1",
				"STRESSBOT_FATAL_LOG_PATH="+path,
				"STRESSBOT_FATAL_LOG_KIND="+kind,
			)
			if err := cmd.Run(); err == nil {
				t.Fatal("fatal helper exited successfully")
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fatal log: %v", err)
			}
			if len(data) == 0 {
				t.Fatal("fatal log was not flushed before process exit")
			}
		})
	}
}

func testLogConfig() *Config {
	return &Config{
		PrintConsole: false,
		LogLevel:     "debug",
		MaxSize:      1,
		MaxBackups:   1,
		MaxAge:       1,
	}
}
