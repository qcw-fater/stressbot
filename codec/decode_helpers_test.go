// Package codec_test — 测试用 schema 变体构造器（仅测试代码）。
//
// 这些 helper 在测试中构造非默认 schema（onError=keep / rol=5 / aes_ecb keyLen=16），
// 证明 Params/KeyLen/onError 字段被引擎读取而非硬编码。
package codec_test

import (
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
	s, err := codec.LoadSchema("testdata/aes_ecb_codec.json")
	if err != nil {
		t.Fatalf("LoadSchema testdata/aes_ecb_codec.json: %v", err)
	}
	c, err := codec.NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec: %v", err)
	}
	return c
}
