package protox

import (
	"fmt"
	"strings"
	"sync"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Registry 消息类型注册表。
// 从 protoregistry.Files 构建按消息全名索引的快速查找表。
type Registry struct {
	mu       sync.RWMutex
	files    *protoregistry.Files
	messages map[string]protoreflect.MessageDescriptor // fullName -> descriptor
}

// NewRegistry 从文件注册表构建消息类型注册表
func NewRegistry(files *protoregistry.Files) *Registry {
	r := &Registry{
		files:    files,
		messages: make(map[string]protoreflect.MessageDescriptor),
	}
	r.indexMessages()
	return r
}

// indexMessages 遍历所有文件，索引消息类型
func (r *Registry) indexMessages() {
	r.files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		indexFromDescriptor(fd.Messages(), r.messages, string(fd.Package()))
		return true
	})
	fmt.Printf("[PROTOX] 已索引 %d 个消息类型\n", len(r.messages))
}

// indexFromDescriptor 递归索引嵌套消息
func indexFromDescriptor(msgs protoreflect.MessageDescriptors, result map[string]protoreflect.MessageDescriptor, pkg string) {
	for i := 0; i < msgs.Len(); i++ {
		md := msgs.Get(i)
		fullName := string(md.FullName())
		result[fullName] = md

		// 同时注册短名（不含包名）
		shortName := fullName
		if idx := strings.LastIndex(fullName, "."); idx >= 0 {
			shortName = fullName[idx+1:]
		}
		// 短名仅在无冲突时注册
		if _, exists := result[shortName]; !exists {
			result[shortName] = md
		}

		// 递归处理嵌套消息
		if md.Messages().Len() > 0 {
			indexFromDescriptor(md.Messages(), result, pkg)
		}
	}
}

// Lookup 查找消息类型描述符
// name 可以是全名（如 "login.PlayerLoginC2S"）或短名（如 "PlayerLoginC2S"）
func (r *Registry) Lookup(name string) (protoreflect.MessageDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	md, ok := r.messages[name]
	return md, ok
}

// Files 返回底层文件注册表
func (r *Registry) Files() *protoregistry.Files {
	return r.files
}

// ListMessages 列出所有已注册的消息类型全名
func (r *Registry) ListMessages() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.messages))
	for name, md := range r.messages {
		// 仅返回全名（包含 . 的）
		if strings.Contains(name, ".") {
			names = append(names, string(md.FullName()))
		}
	}
	return names
}
