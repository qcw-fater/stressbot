// Package codec_test — T1.5 测试用 schema 变体构造器（仅测试代码）。
//
// 这些 helper 在测试中构造非默认 schema（onError=keep / rol=5 / aes_ecb keyLen=16），
// 证明 Params/KeyLen/onError 修复后引擎读取了 schema 字段而非硬编码。
package codec_test

import (
	"path/filepath"
	"testing"

	"stressbot/codec"
)

// newSchemaCodecKeepGzip 加载 tcp_logic_codec.json 并把 gz 步的 onError 改为 "keep"。
func newSchemaCodecKeepGzip(t *testing.T) *codec.SchemaCodec {
	t.Helper()
	s, err := codec.LoadSchema("testdata/tcp_logic_codec.json")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	for i := range s.Pipeline {
		if s.Pipeline[i].Name == "gz" {
			s.Pipeline[i].OnError = "keep"
		}
	}
	c, err := codec.NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec: %v", err)
	}
	return c
}

// newSchemaCodecUDPKeepGzip 构造 UDP 对拍变体：enc offset 改为 {encode:11, decode:0}
// 并把 gz 步的 onError 改为 "keep"。用于 UDP 压缩+加密 对拍缺口（T1.7 carry-over a）：
// codec.lua decode_udp = decode_tcp 在解 UDP-encode 帧（offset 11）时会因 offset 0 解出
// 乱码 gzip 流，pcall 解压失败被吞 → 返回乱码 body（lenient）；engine 的 onError:keep
// 变体在同一帧上做相同事（解密出乱码 → 解压失败 → 保留原字节），证字节一致。
func newSchemaCodecUDPKeepGzip(t *testing.T) *codec.SchemaCodec {
	t.Helper()
	s, err := codec.LoadSchema("testdata/tcp_logic_codec.json")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	for i := range s.Pipeline {
		if s.Pipeline[i].Name == "enc" {
			s.Pipeline[i].Offset = &codec.StepOffset{Encode: 11, Decode: 0}
		}
		if s.Pipeline[i].Name == "gz" {
			s.Pipeline[i].OnError = "keep"
		}
	}
	c, err := codec.NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec: %v", err)
	}
	return c
}

// newSchemaCodecKeepEnc 加载 tcp_logic_codec.json 并把 enc 步的 onError 改为 "keep"。
func newSchemaCodecKeepEnc(t *testing.T) *codec.SchemaCodec {
	t.Helper()
	s, err := codec.LoadSchema("testdata/tcp_logic_codec.json")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	for i := range s.Pipeline {
		if s.Pipeline[i].Name == "enc" {
			s.Pipeline[i].OnError = "keep"
		}
	}
	c, err := codec.NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec: %v", err)
	}
	return c
}

// newSchemaCodecRol 加载 tcp_logic_codec.json 并把 enc 步的 params.rol 改为 rol。
func newSchemaCodecRol(t *testing.T, rol int) *codec.SchemaCodec {
	t.Helper()
	s, err := codec.LoadSchema("testdata/tcp_logic_codec.json")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	for i := range s.Pipeline {
		if s.Pipeline[i].Name == "enc" {
			s.Pipeline[i].Params = map[string]any{"rol": rol}
		}
	}
	c, err := codec.NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec: %v", err)
	}
	return c
}

// newSchemaCodecAesEcb 加载 testdata/aes_ecb_codec.json：aes_ecb cipher + keyLen=16。
func newSchemaCodecAesEcb(t *testing.T) *codec.SchemaCodec {
	t.Helper()
	root := findRepoRoot(t)
	path := filepath.Join(root, "codec", "testdata", "aes_ecb_codec.json")
	s, err := codec.LoadSchema(path)
	if err != nil {
		t.Fatalf("LoadSchema %s: %v", path, err)
	}
	c, err := codec.NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec: %v", err)
	}
	return c
}
