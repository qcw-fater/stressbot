package schema

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stressbot/schemas"
)

func TestSharedSchemaCorpus(t *testing.T) {
	assertCorpus := func(pattern string, wantValid bool) {
		t.Helper()
		files, err := fs.Glob(schemas.Files, pattern)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("corpus %q is empty", pattern)
		}
		for _, name := range files {
			name := name
			t.Run(filepath.Base(name), func(t *testing.T) {
				data, err := schemas.Files.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				var validationErr error
				switch {
				case strings.HasPrefix(filepath.Base(name), "flow-"):
					validationErr = ValidateFlow(data)
				case strings.HasPrefix(filepath.Base(name), "codec-"):
					validationErr = ValidateCodec(data)
				default:
					t.Fatalf("corpus filename must start with flow- or codec-: %s", name)
				}
				if wantValid && validationErr != nil {
					t.Fatalf("valid corpus rejected: %v", validationErr)
				}
				if !wantValid {
					if validationErr == nil {
						t.Fatal("invalid corpus accepted")
					}
					if !strings.Contains(validationErr.Error(), "/") {
						t.Fatalf("validation error has no JSON pointer: %v", validationErr)
					}
				}
			})
		}
	}

	assertCorpus("testdata/valid/*.json", true)
	assertCorpus("testdata/invalid/*.json", false)
}

func TestRepositoryConfigurationConformsToSharedSchemas(t *testing.T) {
	flow, err := os.ReadFile(filepath.Join("..", "conf", "flow", "flow.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateFlow(flow); err != nil {
		t.Fatalf("conf/flow/flow.json: %v", err)
	}

	codecs, err := filepath.Glob(filepath.Join("..", "conf", "adapter", "*_codec.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(codecs) == 0 {
		t.Fatal("no codec configuration found")
	}
	for _, name := range codecs {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateCodec(data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
