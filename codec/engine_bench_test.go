// Package codec_test — T1.7 第 3 项：encode/decode 基准（量化验收，总纲 §0 第 1 项）。
//
// 对比对象：
//   - 新路径：codec.SchemaCodec.EncodeTCP/DecodeTCP（纯 Go，零 gopher-lua）。
//   - 旧路径：adapter.LuaAdapter.EncodeTCP/DecodeTCP（经 LState 池调 codec.lua）。
//
// 矩阵：body 64B（小）/ 2KB（中）/ 16KB（大），加密+压缩组合：
//   - small_rand_64B_encrypt：高熵 64B（< 2048 阈值，不压缩、仅加密）。
//   - med_compressible_2KB_compress_encrypt：低熵 2KB（压缩 + 加密）。
//   - large_compressible_16KB_compress_encrypt：低熵 16KB（压缩 + 加密）。
//
// 报告要求（brief 第 3 项）：记录新 Go 引擎相对旧 Lua 路径的倍率（ns/op + allocs/op），
// 写进 t1-7-report.md。allocs/op 下降、大 body 提速是预期。
package codec_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"stressbot/adapter"
	"stressbot/codec"
)

// benchSizes 是基准矩阵（body 来源 + 名字）。
var benchSizes = []struct {
	name string
	body []byte
}{
	// 高熵 64B：xorshift 伪随机，不压缩（< 2048 阈值），仅加密。
	{"small_rand_64B_encrypt", genBodyBench(64)},
	// 低熵 2KB：单字节重复，gzip 显著变小 → 压缩 + 加密。
	{"med_compressible_2KB_compress_encrypt", bytes.Repeat([]byte{0x41}, 2048)},
	// 低熵 16KB：同上但更大。
	{"large_compressible_16KB_compress_encrypt", bytes.Repeat([]byte{0x41}, 16*1024)},
}

// genBodyBench 与 engine_test genBody 同种子（xorshift32）。
func genBodyBench(n int) []byte {
	b := make([]byte, n)
	state := uint32(0x12345678)
	for i := range b {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		b[i] = byte(state)
	}
	return b
}

// benchKey 32 字节可复现 key。
func benchKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i*7 + 1)
	}
	return k
}

// benchRoute encode 入参 route。
func benchRoute() map[string]any {
	return map[string]any{"cmd": float64(100), "act": float64(7)}
}

// loadBenchSchemaCodec 加载 testdata/tcp_logic_codec.json（encOffset=0/decOffset=0，
// 与 LuaAdapter codec.lua 同语义）。
func loadBenchSchemaCodec(b *testing.B) *codec.SchemaCodec {
	b.Helper()
	s, err := codec.LoadSchema("testdata/tcp_logic_codec.json")
	if err != nil {
		// 回溯找仓库根（兼容从根目录 go test ./...）。
		dir, _ := os.Getwd()
		for range 8 {
			p := filepath.Join(dir, "codec", "testdata", "tcp_logic_codec.json")
			if _, statErr := os.Stat(p); statErr == nil {
				s, err = codec.LoadSchema(p)
				break
			}
			dir = filepath.Dir(dir)
		}
		if err != nil {
			b.Fatalf("LoadSchema: %v", err)
		}
	}
	c, err := codec.NewSchemaCodec(s, nil)
	if err != nil {
		b.Fatalf("NewSchemaCodec: %v", err)
	}
	return c
}

// loadBenchLuaAdapter 构造旧 LuaAdapter（codec.lua + error.lua），pool=2 避死锁。
func loadBenchLuaAdapter(b *testing.B) *adapter.LuaAdapter {
	b.Helper()
	root := findBenchRoot(b)
	codecLua := filepath.Join(root, "conf", "adapter", "codec.lua")
	errorLua := filepath.Join(root, "conf", "adapter", "error.lua")
	a, err := adapter.NewLuaAdapter(2, codecLua, errorLua)
	if err != nil {
		b.Fatalf("NewLuaAdapter: %v", err)
	}
	b.Cleanup(a.Close)
	return a
}

// findBenchRoot 从 CWD 向上找含 conf/adapter/codec.lua 的目录。
func findBenchRoot(b *testing.B) string {
	b.Helper()
	dir, err := os.Getwd()
	if err != nil {
		b.Fatalf("Getwd: %v", err)
	}
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "conf", "adapter", "codec.lua")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	b.Fatalf("未找到 conf/adapter/codec.lua")
	return ""
}

// ---------------------------------------------------------------------------
// 新路径：SchemaCodec
// ---------------------------------------------------------------------------

func BenchmarkSchemaCodec_Encode(b *testing.B) {
	ut := loadBenchSchemaCodec(b)
	key := benchKey()
	route := benchRoute()

	for _, sz := range benchSizes {
		b.Run(sz.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = ut.EncodeTCP(route, sz.body, key)
			}
		})
	}
}

func BenchmarkSchemaCodec_Decode(b *testing.B) {
	ut := loadBenchSchemaCodec(b)
	key := benchKey()
	route := benchRoute()

	for _, sz := range benchSizes {
		// 用 SchemaCodec 自己 encode 出来的帧作 decode 输入（保证是合法可解帧）。
		frame := ut.EncodeTCP(route, sz.body, key)
		b.Run(sz.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _, _ = ut.DecodeTCP(frame, key)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 旧路径：LuaAdapter（对照基准）
// ---------------------------------------------------------------------------

func BenchmarkLuaAdapter_Encode(b *testing.B) {
	oracle := loadBenchLuaAdapter(b)
	key := benchKey()
	route := benchRoute()

	for _, sz := range benchSizes {
		b.Run(sz.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = oracle.EncodeTCP(route, sz.body, key)
			}
		})
	}
}

func BenchmarkLuaAdapter_Decode(b *testing.B) {
	oracle := loadBenchLuaAdapter(b)
	key := benchKey()
	route := benchRoute()

	for _, sz := range benchSizes {
		// 用 oracle 自己 encode 出来的帧作 decode 输入。
		frame := oracle.EncodeTCP(route, sz.body, key)
		if frame == nil {
			b.Fatalf("oracle encode 返回 nil（size=%s）", sz.name)
		}
		b.Run(sz.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _, _ = oracle.DecodeTCP(frame, key)
			}
		})
	}
}
