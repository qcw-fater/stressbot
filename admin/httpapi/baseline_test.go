// Package admin — T3 Batch-1 前置：adapter 基线索引端点测试。
//
// 覆盖 brief §4：
//   - GET /sbot/baseline/adapter/index.json 返回 200、Content-Type: application/json；
//   - 响应数组恰好含 tcp_logic_codec.json / tcp_battle_codec.json / udp_battle_codec.json /
//     errors.json 四个（集合相等，顺序不强制）；
//   - 不含旧 codec.lua / error.lua。
//
// 复用 codec_distribution_test.go 中已搭建的临时目录 + AdminServer 构造方式（setupCodecDistServer）。
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	json "stressbot/internal/jsonx"
)

// TestBaselineCodecIndex_ListsJsonFiles 镜像 handleBaselineCodecIndex：
// 写入 4 份目标 .json + 2 份干扰 .lua 后，索引端点应只列出 4 份 .json（集合相等，排除 .lua）。
func TestBaselineCodecIndex_ListsJsonFiles(t *testing.T) {
	srv, dir, cleanup := setupCodecDistServer(t)
	defer cleanup()

	// 落盘 4 份目标 .json（用 T1.6 产物字节，保证非空、真实可用）
	adapterDir := filepath.Join(dir, "conf/adapter")
	if err := os.MkdirAll(adapterDir, 0o755); err != nil {
		t.Fatalf("mkdir adapter dir: %v", err)
	}
	codecFiles := map[string]string{
		"tcp_logic_codec.json":  "conf/adapter/tcp_logic_codec.json",
		"tcp_battle_codec.json": "conf/adapter/tcp_battle_codec.json",
		"udp_battle_codec.json": "conf/adapter/udp_battle_codec.json",
	}
	for name, rel := range codecFiles {
		if err := os.WriteFile(filepath.Join(adapterDir, name), readCodecDistFile(t, rel), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(adapterDir, "errors.json"), readCodecDistFile(t, "conf/adapter/errors.json"), 0o644); err != nil {
		t.Fatalf("write errors.json: %v", err)
	}
	// 干扰：旧 .lua（索引端点按 .json 过滤，应被自然排除）
	for _, name := range []string{"codec.lua", "error.lua"} {
		if err := os.WriteFile(filepath.Join(adapterDir, name), []byte("-- legacy\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// 发请求
	req := httptest.NewRequest(http.MethodGet, "/sbot/baseline/adapter/index.json", nil)
	rec := httptest.NewRecorder()
	srv.handleBaselineCodecIndex(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected Content-Type application/json*, got %q", ct)
	}

	var got []string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode index response: %v (body=%s)", err, rec.Body.String())
	}

	want := []string{"tcp_logic_codec.json", "tcp_battle_codec.json", "udp_battle_codec.json", "errors.json"}
	gotSorted := append([]string(nil), got...)
	sort.Strings(gotSorted)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if len(gotSorted) != len(wantSorted) {
		t.Fatalf("index size = %d, want %d; got=%v want=%v", len(gotSorted), len(wantSorted), gotSorted, wantSorted)
	}
	for i := range wantSorted {
		if gotSorted[i] != wantSorted[i] {
			t.Fatalf("index mismatch: got=%v want=%v", gotSorted, wantSorted)
		}
	}

	// 明确排除旧 .lua
	for _, legacy := range []string{"codec.lua", "error.lua"} {
		for _, g := range got {
			if g == legacy {
				t.Fatalf("index must not contain legacy %s; got=%v", legacy, got)
			}
		}
	}
}
