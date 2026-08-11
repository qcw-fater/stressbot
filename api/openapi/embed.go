package openapi

import (
	_ "embed"
	"slices"
)

//go:embed admin.yaml
var adminSpec []byte

// AdminSpec 返回管理面 OpenAPI 文档的独立副本，供运行时校验器和契约测试共用。
func AdminSpec() []byte {
	return slices.Clone(adminSpec)
}
