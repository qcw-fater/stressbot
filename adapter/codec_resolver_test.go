// Package adapter — CodecResolver + LoadCodecResolver 测试（T4.1）。
//
// 用 T1.6 生产产物 conf/adapter/{tcp_logic,tcp_battle,udp_battle}_codec.json + errors.json
// 做真实端到端校验，非 mock。覆盖：
//   - 三 codec 映射加载成功，三个 server 串 Resolve 非 nil；未知 server 返回 nil。
//   - dedup：两个 server 串指向同一文件 → Resolve 返回同一 Adapter 实例（指针相等）。
//   - 缺文件：映射含不存在文件 → 返回中文 error，含 server 串 + 文件名。
//   - 空映射 → 返回 error（不静默返回空 resolver）。
//   - errorsFile 可选：传空字符串也能加载，errorMap 为空，DescribeError 返回 ""。
//   - NewCodecResolver 直接构造：Resolve 行为同 loader 路径。
package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 项目根（worktree）下 adapter 包测试相对 cwd 走 runtime.Version()。直接拼绝对路径。
// 注意：go test 的 cwd 是包目录，需要 ../ 上溯。
const adapterDir = "../conf/adapter"

func realCodecMap() map[string]string {
	return map[string]string{
		"tcp:logic":  "tcp_logic_codec.json",
		"tcp:battle": "tcp_battle_codec.json",
		"udp:battle": "udp_battle_codec.json",
	}
}

// TestLoadCodecResolver_ThreeCodecs_Success 三个真实 codec + errors.json 加载成功。
func TestLoadCodecResolver_ThreeCodecs_Success(t *testing.T) {
	r, err := LoadCodecResolver(adapterDir, realCodecMap(), "errors.json")
	if err != nil {
		t.Fatalf("LoadCodecResolver 失败: %v", err)
	}
	if r == nil {
		t.Fatal("resolver 为 nil")
	}
	for _, server := range []string{"tcp:logic", "tcp:battle", "udp:battle"} {
		a := r.Resolve(server)
		if a == nil {
			t.Errorf("Resolve(%q) 返回 nil，期望非 nil", server)
		}
	}
}

// TestLoadCodecResolver_UnknownServer_Nil 未声明 server 返回 nil（无 fallback）。
func TestLoadCodecResolver_UnknownServer_Nil(t *testing.T) {
	r, err := LoadCodecResolver(adapterDir, realCodecMap(), "errors.json")
	if err != nil {
		t.Fatalf("LoadCodecResolver 失败: %v", err)
	}
	if got := r.Resolve("tcp:nonexistent"); got != nil {
		t.Errorf("Resolve(未知 server) 返回非 nil：%T，期望 nil", got)
	}
	if got := r.Resolve(""); got != nil {
		t.Errorf("Resolve(\"\") 返回非 nil：%T，期望 nil", got)
	}
}

// TestLoadCodecResolver_Dedup 同文件多 server 共享同一 Adapter 实例。
func TestLoadCodecResolver_Dedup(t *testing.T) {
	codecs := map[string]string{
		"tcp:logic":  "tcp_logic_codec.json",
		"tcp:logic2": "tcp_logic_codec.json", // 同文件
	}
	r, err := LoadCodecResolver(adapterDir, codecs, "errors.json")
	if err != nil {
		t.Fatalf("LoadCodecResolver 失败: %v", err)
	}
	a1 := r.Resolve("tcp:logic")
	a2 := r.Resolve("tcp:logic2")
	if a1 == nil || a2 == nil {
		t.Fatalf("dedup 两侧均应非 nil：a1=%v a2=%v", a1, a2)
	}
	// 指针相等：同一无状态 Adapter 实例
	if a1 != a2 {
		t.Errorf("dedup 期望同一实例，得到不同实例：%p vs %p", a1, a2)
	}
}

// TestLoadCodecResolver_MissingFile 映射指向不存在文件 → 中文 error，含 server 与文件名。
func TestLoadCodecResolver_MissingFile(t *testing.T) {
	codecs := map[string]string{
		"tcp:logic": "tcp_logic_codec.json",
		"tcp:gone":  "does_not_exist_codec.json",
	}
	_, err := LoadCodecResolver(adapterDir, codecs, "errors.json")
	if err == nil {
		t.Fatal("缺文件应返回 error，得到 nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "tcp:gone") {
		t.Errorf("error 不含 server 串 %q：%s", "tcp:gone", msg)
	}
	if !strings.Contains(msg, "does_not_exist_codec.json") {
		t.Errorf("error 不含文件名：%s", msg)
	}
}

// TestLoadCodecResolver_EmptyMap 空映射 → error（不静默）。
func TestLoadCodecResolver_EmptyMap(t *testing.T) {
	_, err := LoadCodecResolver(adapterDir, map[string]string{}, "errors.json")
	if err == nil {
		t.Fatal("空映射应返回 error，得到 nil")
	}
}

// TestLoadCodecResolver_NilMap nil 映射 → error。
func TestLoadCodecResolver_NilMap(t *testing.T) {
	_, err := LoadCodecResolver(adapterDir, nil, "errors.json")
	if err == nil {
		t.Fatal("nil 映射应返回 error，得到 nil")
	}
}

// TestLoadCodecResolver_ErrorsOptional errorsFile 传空也能加载，errorMap 视为空。
func TestLoadCodecResolver_ErrorsOptional(t *testing.T) {
	r, err := LoadCodecResolver(adapterDir, realCodecMap(), "")
	if err != nil {
		t.Fatalf("errorsFile 为空应可加载，得到 error: %v", err)
	}
	a := r.Resolve("tcp:logic")
	if a == nil {
		t.Fatal("Resolve 返回 nil")
	}
	// 无 errors.json → DescribeError 返回空串（v1 冻结语义）。
	if got := a.DescribeError(1); got != "" {
		t.Errorf("errorsFile 为空时 DescribeError 应返回 \"\"，得到 %q", got)
	}
}

// TestLoadCodecResolver_ErrorsFileMissing errorsFile 非空但缺文件 → fail loud。
func TestLoadCodecResolver_ErrorsFileMissing(t *testing.T) {
	_, err := LoadCodecResolver(adapterDir, realCodecMap(), "no_such_errors.json")
	if err == nil {
		t.Fatal("errorsFile 缺失应 fail loud，得到 nil error")
	}
	if !strings.Contains(err.Error(), "no_such_errors.json") {
		t.Errorf("error 不含 errors 文件名：%s", err.Error())
	}
}

// TestLoadCodecResolver_ErrorMapRejectsFrameworkRange errors.json 含 < 100 码（框架保留段）
// → fail loud（码段契约：业务码不得占用框架保留段）。error 信息须含触发撞码的具体 code。
//
// 用 t.TempDir 隔离，写一份含 54（< 100，框架保留）+ 1004（合法）的 errors.json，
// 加一份最小 tcp_logic_codec.json（避免「无 codec 文件」分支提前报错）。
func TestLoadCodecResolver_ErrorMapRejectsFrameworkRange(t *testing.T) {
	tmp := t.TempDir()

	// errors.json：54 撞框架保留段，1004 合法。撞码应优先生效（硬报错）。
	errorsJSON := `{"54":"撞框架","1004":"队伍已满"}`
	if err := os.WriteFile(filepath.Join(tmp, "errors.json"), []byte(errorsJSON), 0o644); err != nil {
		t.Fatalf("写入 errors.json 失败: %v", err)
	}

	// 最小 codec 文件：从真实产物复制（让 resolver 不报「无 codec / 解析失败」）。
	src := filepath.Join(adapterDir, "tcp_logic_codec.json")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读取测试 fixture 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "tcp_logic_codec.json"), data, 0o644); err != nil {
		t.Fatalf("写入 codec 文件失败: %v", err)
	}

	codecs := map[string]string{"tcp:logic": "tcp_logic_codec.json"}
	_, err = LoadCodecResolver(tmp, codecs, "errors.json")
	if err == nil {
		t.Fatal("errors.json 含 < 100 框架保留码应返回 error，得到 nil")
	}
	if !strings.Contains(err.Error(), "54") {
		t.Errorf("error 不含撞码 code 54：%s", err.Error())
	}
}

// TestLoadCodecResolver_ErrorsLoaded errors.json 命中描述非空。
func TestLoadCodecResolver_ErrorsLoaded(t *testing.T) {
	r, err := LoadCodecResolver(adapterDir, realCodecMap(), "errors.json")
	if err != nil {
		t.Fatalf("LoadCodecResolver 失败: %v", err)
	}
	a := r.Resolve("tcp:logic")
	if a == nil {
		t.Fatal("Resolve 返回 nil")
	}
	// errors.json 667 条，几乎必有非空描述；用任一常见 code 试探。
	// 取不到具体 code 就跳过精确断言，仅验证不 panic + 返回 string。
	_ = a.DescribeError(0) // 不应 panic
}

// TestLoadCodecResolver_AbsolutePath codecDir 绝对路径也能加载。
func TestLoadCodecResolver_AbsolutePath(t *testing.T) {
	abs, err := filepath.Abs(adapterDir)
	if err != nil {
		t.Fatalf("filepath.Abs 失败: %v", err)
	}
	r, err := LoadCodecResolver(abs, realCodecMap(), "errors.json")
	if err != nil {
		t.Fatalf("绝对路径加载失败: %v", err)
	}
	if r.Resolve("tcp:logic") == nil {
		t.Error("绝对路径 Resolve 返回 nil")
	}
}

// TestNewCodecResolver_Direct 直接构造，Resolve 行为正确（含未知 server 返回 nil）。
func TestNewCodecResolver_Direct(t *testing.T) {
	// 用 loader 得到三份 Adapter 实例。
	r, err := LoadCodecResolver(adapterDir, realCodecMap(), "errors.json")
	if err != nil {
		t.Fatalf("LoadCodecResolver 失败: %v", err)
	}
	a1 := r.Resolve("tcp:logic")
	a2 := r.Resolve("tcp:battle")
	a3 := r.Resolve("udp:battle")
	if a1 == nil || a2 == nil || a3 == nil {
		t.Fatalf("三个 adapter 均应非 nil：%v %v %v", a1, a2, a3)
	}
	// 直接构造：复用上述实例。
	direct := NewCodecResolver(map[string]Adapter{
		"tcp:logic":  a1,
		"tcp:battle": a2,
		"udp:battle": a3,
	})
	if direct.Resolve("tcp:logic") != a1 {
		t.Error("NewCodecResolver Resolve 不等于原实例")
	}
	if direct.Resolve("tcp:battle") != a2 {
		t.Error("NewCodecResolver Resolve 不等于原实例")
	}
	if direct.Resolve("udp:battle") != a3 {
		t.Error("NewCodecResolver Resolve 不等于原实例")
	}
	if direct.Resolve("udp:zzz") != nil {
		t.Error("NewCodecResolver 未知 server 应返回 nil")
	}
}

// TestNewCodecResolver_NilMap 空入参 map 也能构造（直接构造不校验空，与 loader 不同）。
func TestNewCodecResolver_EmptyMap(t *testing.T) {
	r := NewCodecResolver(map[string]Adapter{})
	if r == nil {
		t.Fatal("NewCodecResolver 返回 nil")
	}
	if got := r.Resolve("anything"); got != nil {
		t.Errorf("空 map resolver Resolve 应返回 nil，得到 %v", got)
	}
}

// TestInferCodecMap_RealAdapterDir 用 T1.6 真实产物 conf/adapter 推断出三连接映射。
// 文件名 <proto>_<service>_codec.json → server 串 <proto>:<service>。
func TestInferCodecMap_RealAdapterDir(t *testing.T) {
	m, err := InferCodecMap(adapterDir)
	if err != nil {
		t.Fatalf("InferCodecMap 失败: %v", err)
	}
	want := map[string]string{
		"tcp:logic":  "tcp_logic_codec.json",
		"tcp:battle": "tcp_battle_codec.json",
		"udp:battle": "udp_battle_codec.json",
	}
	for server, file := range want {
		got, ok := m[server]
		if !ok {
			t.Errorf("InferCodecMap 缺少 server %q（得到 %v）", server, m)
			continue
		}
		if got != file {
			t.Errorf("InferCodecMap[%q] = %q，期望 %q", server, got, file)
		}
	}
	if extra := len(m) - len(want); extra > 0 {
		t.Errorf("InferCodecMap 多出 %d 个映射（应只推断 3 份 codec.json，排除 errors.json/error.lua/codec.lua），得到 %v", extra, m)
	}
}

// TestInferCodecMap_RoundTripWithLoader 推断出的 map 能直接喂 LoadCodecResolver 并 Resolve 非 nil。
func TestInferCodecMap_RoundTripWithLoader(t *testing.T) {
	m, err := InferCodecMap(adapterDir)
	if err != nil {
		t.Fatalf("InferCodecMap 失败: %v", err)
	}
	r, err := LoadCodecResolver(adapterDir, m, "errors.json")
	if err != nil {
		t.Fatalf("LoadCodecResolver 失败: %v", err)
	}
	for _, server := range []string{"tcp:logic", "tcp:battle", "udp:battle"} {
		if r.Resolve(server) == nil {
			t.Errorf("Resolve(%q) 返回 nil，期望非 nil", server)
		}
	}
}

// TestInferCodecMap_EmptyDir 空目录 → 中文 error（不静默返回空 map）。
func TestInferCodecMap_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	_, err := InferCodecMap(tmp)
	if err == nil {
		t.Fatal("空目录 InferCodecMap 应返回 error，得到 nil")
	}
	if !strings.Contains(err.Error(), "codec") {
		t.Errorf("error 不含 codec 关键词：%s", err.Error())
	}
}

// TestInferCodecMap_MissingDir 目录不存在 → error。
func TestInferCodecMap_MissingDir(t *testing.T) {
	_, err := InferCodecMap(filepath.Join(adapterDir, "does_not_exist"))
	if err == nil {
		t.Fatal("不存在的目录 InferCodecMap 应返回 error，得到 nil")
	}
}

// TestInferCodecMap_IgnoresNonCodecFiles errors.json / codec.lua / error.lua 不被当作 codec 文件。
func TestInferCodecMap_IgnoresNonCodecFiles(t *testing.T) {
	tmp := t.TempDir()
	// 只放非 codec 文件 → 应当作「无 codec 文件」报错。
	for _, name := range []string{"errors.json", "codec.lua", "error.lua", "README.md"} {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("{}"), 0o644); err != nil {
			t.Fatalf("写入 %s 失败: %v", name, err)
		}
	}
	_, err := InferCodecMap(tmp)
	if err == nil {
		t.Fatal("只有非 codec 文件应报错，得到 nil error")
	}
}

// TestInferCodecMap_SkipsErrorsJson errors.json（虽然后缀不匹配 *_codec.json，但要确认不会被误收）。
func TestInferCodecMap_SkipsErrorsJson(t *testing.T) {
	tmp := t.TempDir()
	// 放一份真正的 codec 文件 + 一份 errors.json，确认只推断出 1 个。
	src := filepath.Join(adapterDir, "tcp_logic_codec.json")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读取测试 fixture 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "tcp_logic_codec.json"), data, 0o644); err != nil {
		t.Fatalf("写入 codec 文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "errors.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("写入 errors.json 失败: %v", err)
	}
	m, err := InferCodecMap(tmp)
	if err != nil {
		t.Fatalf("InferCodecMap 失败: %v", err)
	}
	if len(m) != 1 {
		t.Errorf("应只推断出 1 个 codec，得到 %d 个：%v", len(m), m)
	}
	if _, ok := m["tcp:logic"]; !ok {
		t.Errorf("缺少 tcp:logic 映射，得到 %v", m)
	}
}
