package runner

import (
	"stressbot/network"
	"stressbot/protocol"
)

// StartDialer 使用已加载协议元信息启动网络引擎。engine 零值 = 编译期默认参数。
func StartDialer(resources *Resources, engine network.EngineConfig) (*network.Dialer, error) {
	dialer := network.NewDialer(protocol.PickMetaAdapter(resources.Resolver, resources.CodecMap), engine)
	if err := dialer.Start(); err != nil {
		return nil, err
	}
	return dialer, nil
}
