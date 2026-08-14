// Package protox 提供动态 Protobuf 消息加载和操作。
// 通过 protocompile 在运行时解析 .proto 文件，使用 dynamicpb 创建动态消息，
// 通过 protoreflect 进行字段读写，无需生成 Go 代码。
package protox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"stressbot/internal/stresslog"
	"strings"

	"github.com/bufbuild/protocompile"
	"go.uber.org/zap"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Loader .proto 文件加载器。
// 扫描指定目录下所有 .proto 文件，使用 protocompile 编译为 FileDescriptor。
type Loader struct {
	protoDirs  []string // .proto 文件搜索目录
	protoFiles []string // 指定加载的文件（为空则加载目录下所有）
}

// NewLoader 创建 proto 加载器。
// protoDirs 为 .proto 文件所在目录列表。
// protoFiles 为指定的 .proto 文件（相对路径），为空则加载所有。
func NewLoader(protoDirs []string, protoFiles []string) *Loader {
	return &Loader{
		protoDirs:  protoDirs,
		protoFiles: protoFiles,
	}
}

// Load 加载并编译所有 .proto 文件，返回文件注册表。
func (l *Loader) Load() (*protoregistry.Files, error) {
	// 收集需要编译的 .proto 文件
	files, err := l.collectFiles()
	if err != nil {
		return nil, fmt.Errorf("收集 proto 文件失败: %w", err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("未找到任何 .proto 文件")
	}

	// 构建 resolver：先从源文件目录查找，再回退到标准 proto 导入
	sourceResolver := &protocompile.SourceResolver{
		ImportPaths: l.protoDirs,
	}
	resolver := protocompile.WithStandardImports(sourceResolver)

	// 使用 protocompile 编译
	compiler := &protocompile.Compiler{
		Resolver: resolver,
	}

	descs, err := compiler.Compile(context.Background(), files...)
	if err != nil {
		return nil, fmt.Errorf("编译 proto 文件失败: %w", err)
	}

	// 构建 protoregistry.Files
	reg := &protoregistry.Files{}
	for _, desc := range descs {
		if err := reg.RegisterFile(desc); err != nil {
			return nil, fmt.Errorf("注册 proto 文件 %s 失败: %w", desc.Path(), err)
		}
	}

	logLoaded(reg)
	return reg, nil
}

// collectFiles 收集需要编译的 .proto 文件列表
func (l *Loader) collectFiles() ([]string, error) {
	if len(l.protoFiles) > 0 {
		return l.protoFiles, nil
	}

	// 扫描所有目录下的 .proto 文件
	var files []string
	for _, dir := range l.protoDirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ".proto") {
				// 转为相对于 protoDirs 的相对路径
				rel, err := filepath.Rel(dir, path)
				if err != nil {
					rel = path
				}
				// 使用正斜杠（proto 路径要求）
				files = append(files, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// 去重
	seen := make(map[string]bool)
	unique := make([]string, 0, len(files))
	for _, f := range files {
		if !seen[f] {
			seen[f] = true
			unique = append(unique, f)
		}
	}

	return unique, nil
}

// logLoaded 打印加载的 proto 文件信息
func logLoaded(reg *protoregistry.Files) {
	count := 0
	reg.RangeFiles(func(_ protoreflect.FileDescriptor) bool {
		count++
		return true
	})
	stresslog.Info("[PROTOX] 已加载 proto 文件", zap.Int("count", count))
}
