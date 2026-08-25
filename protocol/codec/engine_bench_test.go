// Package codec_test — encode/decode 基准（纯 Go SchemaCodec）。
//
// 矩阵：body 64B（小）/ 2KB（中）/ 16KB（大），加密+压缩组合：
//   - small_rand_64B_encrypt：高熵 64B（< 2048 阈值，不压缩、仅加密）。
//   - med_compressible_2KB_compress_encrypt：低熵 2KB（压缩 + 加密）。
//   - large_compressible_16KB_compress_encrypt：低熵 16KB（压缩 + 加密）。
package codec_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"stressbot/protocol/codec"
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

// loadBenchSchemaCodec 加载 testdata/tcp_logic_codec.json（encOffset=0/decOffset=0）。
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

func BenchmarkSchemaCodec_Encode(b *testing.B) {
	ut := loadBenchSchemaCodec(b)
	key := benchKey()
	route := benchRoute()

	for _, sz := range benchSizes {
		b.Run(sz.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
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
			for range b.N {
				_, _, _ = ut.DecodeTCP(frame, key)
			}
		})
	}
}
