package engine

import (
	"strings"
	"testing"
)

func TestResolveRandomStringCharsetAliases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty defaults to alphanum", input: "", want: randomStringCharsetAlphanum},
		{name: "whitespace defaults to alphanum", input: "   ", want: randomStringCharsetAlphanum},
		{name: "lower alias", input: "lower", want: randomStringCharsetLower},
		{name: "upper alias", input: "upper", want: randomStringCharsetUpper},
		{name: "alpha alias", input: "alpha", want: randomStringCharsetAlpha},
		{name: "numeric alias", input: "numeric", want: randomStringCharsetNumeric},
		{name: "alphanum alias", input: "alphanum", want: randomStringCharsetAlphanum},
		{name: "custom literal", input: "ABC-123_", want: "ABC-123_"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveRandomStringCharset(tt.input); got != tt.want {
				t.Fatalf("resolveRandomStringCharset(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRandomStringCharsetUsesResolvedAliasCharacters(t *testing.T) {
	tests := []struct {
		name    string
		alias   string
		allowed string
	}{
		{name: "lower", alias: "lower", allowed: randomStringCharsetLower},
		{name: "upper", alias: "upper", allowed: randomStringCharsetUpper},
		{name: "numeric", alias: "numeric", allowed: randomStringCharsetNumeric},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := randomStringCharset(256, resolveRandomStringCharset(tt.alias))
			if len(got) != 256 {
				t.Fatalf("len(randomStringCharset(...)) = %d, want 256", len(got))
			}
			for _, ch := range got {
				if !strings.ContainsRune(tt.allowed, ch) {
					t.Fatalf("generated %q from alias %q, allowed charset %q", ch, tt.alias, tt.allowed)
				}
			}
		})
	}
}

func TestRandomStringCharsetUsesCustomLiteralCharacters(t *testing.T) {
	const custom = "AB-"
	got := randomStringCharset(128, resolveRandomStringCharset(custom))
	if len(got) != 128 {
		t.Fatalf("len(randomStringCharset(...)) = %d, want 128", len(got))
	}
	for _, ch := range got {
		if !strings.ContainsRune(custom, ch) {
			t.Fatalf("generated %q outside custom charset %q", ch, custom)
		}
	}
}
