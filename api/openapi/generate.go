// Package openapi 固定 stressbot OpenAPI 生成入口。
package openapi

//go:generate go tool oapi-codegen -config oapi-codegen-control-plane.yaml control-plane.yaml
//go:generate go tool oapi-codegen -config oapi-codegen-admin.yaml admin.yaml
