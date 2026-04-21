package network

import (
	"fmt"
	"sync"
)

// middlewareRegistry 全局命名中间件注册表。
// 用户通过 Lua 脚本、RegisterMiddleware 或 RegisterStandard 显式注册。
var middlewareRegistry = &namedRegistry{
	factories: map[string]func(ProtocolConfig) PacketMiddleware{},
}

// standardMiddleware 框架提供的标准中间件。
// 这些是通用/标准算法（非游戏特有），用户按需注册。
var standardMiddleware = map[string]func(ProtocolConfig) PacketMiddleware{
	"gzip": GzipMiddleware,
}

type namedRegistry struct {
	mu        sync.RWMutex
	factories map[string]func(ProtocolConfig) PacketMiddleware
}

func (r *namedRegistry) register(name string, factory func(ProtocolConfig) PacketMiddleware) {
	r.mu.Lock()
	r.factories[name] = factory
	r.mu.Unlock()
}

func (r *namedRegistry) get(name string) (func(ProtocolConfig) PacketMiddleware, bool) {
	r.mu.RLock()
	f := r.factories[name]
	r.mu.RUnlock()
	return f, f != nil
}

// RegisterMiddleware 注册命名中间件工厂。
// 在调用 NewProtocol 之前调用。工厂函数接收 ProtocolConfig 以便读取配置参数。
// 注册后可在 header.json 的 middleware 数组中通过名称引用。
func RegisterMiddleware(name string, factory func(ProtocolConfig) PacketMiddleware) {
	middlewareRegistry.register(name, factory)
}

// RegisterStandard 注册框架提供的标准中间件。
// 可用名称: "gzip" 等。返回是否注册成功。
func RegisterStandard(name string) bool {
	factory, ok := standardMiddleware[name]
	if !ok {
		return false
	}
	RegisterMiddleware(name, factory)
	return true
}

// ListStandard 返回可用的标准中间件名称。
func ListStandard() []string {
	names := make([]string, 0, len(standardMiddleware))
	for name := range standardMiddleware {
		names = append(names, name)
	}
	return names
}

// resolveMiddleware 按名称查找中间件工厂，不存在则 panic。
func resolveMiddleware(name string, cfg ProtocolConfig) PacketMiddleware {
	factory, ok := middlewareRegistry.get(name)
	if !ok {
		panic(fmt.Sprintf("unknown middleware: %q (registered: %v, standard: %v)",
			name, listMiddlewareNames(), ListStandard()))
	}
	return factory(cfg)
}

func listMiddlewareNames() []string {
	middlewareRegistry.mu.RLock()
	defer middlewareRegistry.mu.RUnlock()
	names := make([]string, 0, len(middlewareRegistry.factories))
	for name := range middlewareRegistry.factories {
		names = append(names, name)
	}
	return names
}
