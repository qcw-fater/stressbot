package protox

import (
	"strings"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Registry 消息类型注册表。
// 从 protoregistry.Files 构建按消息全名索引的快速查找表。
// 构建后 messages 只读，无需加锁。
type Registry struct {
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
	stresslog.Info("[PROTOX] 已索引消息类型", zap.Int("count", len(r.messages)))
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
// name 可以是全名（如 "example.RequestC2S"）或短名（如 "PlayerLoginC2S"）
func (r *Registry) Lookup(name string) (protoreflect.MessageDescriptor, bool) {
	md, ok := r.messages[name]
	return md, ok
}
