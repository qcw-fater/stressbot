package grpcapi

import (
	"time"

	controlpb "stressbot/controlplane/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

const maxControlMessageSize = 16 << 20

// NewServer 构造 Admin gRPC 服务器：注册 Agent 控制、资源包与指标三个
// 服务，并设置 16MB 消息上限与 keepalive 参数（空闲连接回收、长连接保活、
// 防激进客户端）。不设置 MaxConnectionAge：压测任务没有时长上限，控制面
// 连接必须能无限期存活，grpc-go 默认也不限制连接年龄。
func NewServer(deps Dependencies) *grpc.Server {
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxControlMessageSize),
		grpc.MaxSendMsgSize(maxControlMessageSize),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 30 * time.Minute,
			Time:              2 * time.Minute,
			Timeout:           20 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: time.Minute, PermitWithoutStream: true}),
	)
	controlpb.RegisterAgentControlServiceServer(server, &grpcControlService{deps: deps})
	controlpb.RegisterAgentBundleServiceServer(server, &grpcBundleService{store: deps.Bundles})
	controlpb.RegisterAgentMetricsServiceServer(server, &grpcMetricsService{deps: deps})
	return server
}
