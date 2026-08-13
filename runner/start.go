package runner

import (
	"time"

	"stressbot/network"
	"stressbot/protocol"
)

// StartDialer 使用已加载协议元信息启动网络引擎。
func StartDialer(resources *Resources, heartbeatInterval time.Duration) (*network.Dialer, error) {
	dialer := network.NewDialer(protocol.PickMetaAdapter(resources.Resolver, resources.CodecMap), heartbeatInterval)
	if err := dialer.Start(); err != nil {
		return nil, err
	}
	return dialer, nil
}
