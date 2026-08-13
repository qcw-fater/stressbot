package architecturetest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionPackagesDoNotReExportTypeAliases(t *testing.T) {
	root := repositoryRoot(t)
	for _, dir := range []string{"admin", "agent", "engine"} {
		walkProductionGoFiles(t, filepath.Join(root, dir), func(path string, file *ast.File, fset *token.FileSet) {
			ast.Inspect(file, func(node ast.Node) bool {
				typeSpec, ok := node.(*ast.TypeSpec)
				if !ok || !typeSpec.Assign.IsValid() {
					return true
				}
				position := fset.Position(typeSpec.Pos())
				rel, err := filepath.Rel(root, path)
				if err != nil {
					t.Fatal(err)
				}
				t.Errorf("生产包不得转发类型别名: %s:%d %s", filepath.ToSlash(rel), position.Line, typeSpec.Name.Name)
				return true
			})
		})
	}
}

func TestEngineDoesNotReExportActionErrorConstructor(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "engine", "errors.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		valueSpec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for _, name := range valueSpec.Names {
			if name.Name == "NewActionError" {
				t.Errorf("engine 不得转发 errcode.NewActionError: %s", fset.Position(name.Pos()))
			}
		}
		return true
	})
}

func TestMetricsCodeDoesNotUseTelemetryName(t *testing.T) {
	root := repositoryRoot(t)
	for _, dir := range []string{"admin", "agent", "controlplane"} {
		walkProductionGoFiles(t, filepath.Join(root, dir), func(path string, file *ast.File, fset *token.FileSet) {
			ast.Inspect(file, func(node ast.Node) bool {
				ident, ok := node.(*ast.Ident)
				if ok && ident.Name == "TelemetrySink" {
					rel, err := filepath.Rel(root, path)
					if err != nil {
						t.Fatal(err)
					}
					t.Errorf("指标代码不得使用 TelemetrySink 旧名: %s:%d", filepath.ToSlash(rel), fset.Position(ident.Pos()).Line)
				}
				return true
			})
		})
	}
}

func TestHTTPRoutesAreSplitByResource(t *testing.T) {
	root := repositoryRoot(t)
	dir := filepath.Join(root, "admin", "httpapi")
	for _, name := range []string{"task_routes.go", "agent_routes.go", "metrics_routes.go", "baseline_routes.go"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("HTTP 路由职责文件缺失 %s: %v", name, err)
		}
	}
	routesPath := filepath.Join(dir, "routes.go")
	fset := token.NewFileSet()
	routes, err := parser.ParseFile(fset, routesPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]struct{}{
		"handleCreateTask":         {},
		"handleListAgents":         {},
		"handleGetMetrics":         {},
		"handleBaselineProtoIndex": {},
	}
	for _, decl := range routes.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if _, exists := forbidden[funcDecl.Name.Name]; exists {
			t.Errorf("routes.go 仍定义资源 handler %s", funcDecl.Name.Name)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func walkProductionGoFiles(t *testing.T, root string, visit func(string, *ast.File, *token.FileSet)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		visit(path, file, fset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
