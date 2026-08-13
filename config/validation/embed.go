package validation

import "embed"

// Files embeds the shared JSON Schema documents and their validation corpus.
// It is consumed by the Go validator and differential corpus tests.
//
//go:embed *.schema.json testdata/valid/*.json testdata/invalid/*.json
var Files embed.FS
