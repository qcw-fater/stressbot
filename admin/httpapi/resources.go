package httpapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type resourceKind string

const (
	resourceProto   resourceKind = "proto"
	resourceScripts resourceKind = "scripts"
	resourceAdapter resourceKind = "adapter"
)

type resourceStore struct{ root string }

func newBaselineResources(root string) resourceStore { return resourceStore{root: root} }

func (r resourceStore) List(kind resourceKind, extension string) ([]string, error) {
	dir := filepath.Join(r.root, string(kind))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取目录 %s 失败: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), extension) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func (r resourceStore) File(kind resourceKind, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("缺少文件名")
	}
	base := filepath.Base(name)
	if base == "." || base == ".." || base != name {
		return "", fmt.Errorf("文件名无效")
	}
	return filepath.Join(r.root, string(kind), base), nil
}

func (r resourceStore) FlowFile() string { return filepath.Join(r.root, "flow", "flow.json") }
