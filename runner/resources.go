package runner

import (
	"fmt"
	"os"
	"path/filepath"

	"stressbot/config/validation"
	"stressbot/flow"
	json "stressbot/internal/jsonx"
	"stressbot/protocol"
	"stressbot/protocol/protox"
	"stressbot/script"
)

// Resources 保存一次运行共享的协议、protobuf 与流程资源。
type Resources struct {
	Flow          *flow.TaskFlow
	Resolver      protocol.CodecResolver
	CodecMap      map[string]string
	Factory       *protox.Factory
	HasErrorsFile bool
}

// LoadResources 加载并校验 codec、proto 和流程配置。
func LoadResources(paths ResourcePaths) (*Resources, error) {
	codecMap, err := protocol.InferCodecMap(paths.Adapter)
	if err != nil {
		return nil, fmt.Errorf("推断 codec 映射失败: %w", err)
	}
	errorsFile := "errors.json"
	hasErrorsFile := true
	if _, err := os.Stat(filepath.Join(paths.Adapter, errorsFile)); err != nil {
		errorsFile = ""
		hasErrorsFile = false
	}
	resolver, err := protocol.LoadCodecResolver(paths.Adapter, codecMap, errorsFile)
	if err != nil {
		return nil, fmt.Errorf("加载 CodecResolver 失败: %w", err)
	}

	loader := protox.NewLoader([]string{paths.Proto}, nil)
	files, err := loader.Load()
	if err != nil {
		return nil, fmt.Errorf("加载 proto 文件失败: %w", err)
	}
	factory := protox.NewFactory(protox.NewRegistry(files))

	taskFlow, err := loadFlow(paths.Flow)
	if err != nil {
		factory.Close()
		return nil, fmt.Errorf("加载流程配置失败: %w", err)
	}
	for name, action := range taskFlow.Actions {
		action.Name = name
	}

	return &Resources{
		Flow:          taskFlow,
		Resolver:      resolver,
		CodecMap:      codecMap,
		Factory:       factory,
		HasErrorsFile: hasErrorsFile,
	}, nil
}

// Close 释放任务级 protobuf 工厂及其缓存注册。
func (r *Resources) Close() {
	if r != nil && r.Factory != nil {
		r.Factory.Close()
	}
}

// NewRuntimePool 创建 Lua 运行时池并预编译脚本。
// 预编译错误由调用方决定是否降级为告警。
func NewRuntimePool(scriptsDir string) (*script.RuntimePool, error) {
	pool := script.NewRuntimePool(scriptsDir)
	return pool, pool.PrecompileScripts([]string{scriptsDir})
}

func loadFlow(path string) (*flow.TaskFlow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取流程文件: %w", err)
	}
	if err := validation.ValidateFlow(data); err != nil {
		return nil, err
	}
	taskFlow := &flow.TaskFlow{}
	if err := json.Unmarshal(data, taskFlow); err != nil {
		return nil, fmt.Errorf("解析流程文件: %w", err)
	}
	if err := flow.PrepareTaskFlow(taskFlow); err != nil {
		return nil, fmt.Errorf("准备流程条件: %w", err)
	}
	return taskFlow, nil
}
