// Package schema validates stressbot configuration structure before domain decoding.
package schema

import (
	"bytes"
	"fmt"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"stressbot/schemas"
)

const (
	flowSchemaID  = "https://stressbot.local/schemas/flow.schema.json"
	codecSchemaID = "https://stressbot.local/schemas/codec.schema.json"
)

var compiledSchemas struct {
	sync.Once
	flow  *jsonschema.Schema
	codec *jsonschema.Schema
	err   error
}

// ValidateFlow validates the structural contract only; graph references and business invariants remain in engine.
func ValidateFlow(data []byte) error {
	flow, _, err := schemasOnce()
	if err != nil {
		return err
	}
	return validate("flow", flow, data)
}

// ValidateCodec validates the structural contract only; algorithm and pipeline reference checks remain in codec.
func ValidateCodec(data []byte) error {
	_, codec, err := schemasOnce()
	if err != nil {
		return err
	}
	return validate("codec", codec, data)
}

func schemasOnce() (*jsonschema.Schema, *jsonschema.Schema, error) {
	compiledSchemas.Do(func() {
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		for _, resource := range []struct {
			name string
			id   string
		}{
			{name: "flow.schema.json", id: flowSchemaID},
			{name: "codec.schema.json", id: codecSchemaID},
		} {
			data, err := schemas.Files.ReadFile(resource.name)
			if err != nil {
				compiledSchemas.err = fmt.Errorf("读取内嵌 JSON Schema %s 失败: %w", resource.name, err)
				return
			}
			document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
			if err != nil {
				compiledSchemas.err = fmt.Errorf("解析内嵌 JSON Schema %s 失败: %w", resource.name, err)
				return
			}
			if err := compiler.AddResource(resource.id, document); err != nil {
				compiledSchemas.err = fmt.Errorf("注册内嵌 JSON Schema %s 失败: %w", resource.name, err)
				return
			}
		}
		compiledSchemas.flow, compiledSchemas.err = compiler.Compile(flowSchemaID)
		if compiledSchemas.err != nil {
			compiledSchemas.err = fmt.Errorf("编译 flow JSON Schema 失败: %w", compiledSchemas.err)
			return
		}
		compiledSchemas.codec, compiledSchemas.err = compiler.Compile(codecSchemaID)
		if compiledSchemas.err != nil {
			compiledSchemas.err = fmt.Errorf("编译 codec JSON Schema 失败: %w", compiledSchemas.err)
		}
	})
	return compiledSchemas.flow, compiledSchemas.codec, compiledSchemas.err
}

func validate(kind string, compiled *jsonschema.Schema, data []byte) error {
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%s 配置不是合法 JSON: %w", kind, err)
	}
	if err := compiled.Validate(value); err != nil {
		return fmt.Errorf("%s 配置结构校验失败: %w", kind, err)
	}
	return nil
}
