// Package schemas owns the shared JSON Schema documents and their conformance corpus.
package schemas

import "embed"

// Files is consumed by both the Go validator and its differential corpus tests.
//
//go:embed *.schema.json testdata/valid/*.json testdata/invalid/*.json
var Files embed.FS
