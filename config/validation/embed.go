package validation

import "embed"

// Files 内嵌共享的 JSON Schema 文档及其校验语料，
// 供 Go 校验器与语料差分测试使用。
//
//go:embed *.schema.json testdata/valid/*.json testdata/invalid/*.json
var Files embed.FS
