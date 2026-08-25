package grpcapi

import (
	"time"

	controlpb "stressbot/controlplane/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

const maxControlMessageSize = 16 << 20

// NewServer 构造 Admin gRPC 服务器：注册 Agent 控制、资源包与指标三个
// 服务，并设置 16MB 消息上限与 keepalive 参数（长连接保活、防激进客户端）。
func NewServer(deps Dependencies) *grpc.Server {
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxControlMessageSize),
		grpc.MaxSendMsgSize(maxControlMessageSize),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     30 * time.Minute,
			MaxConnectionAge:      12 * time.Hour,
			MaxConnectionAgeGrace: time.Minute,
			Time:                  2 * time.Minute,
			Timeout:               20 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: time.Minute, PermitWithoutStream: true}),
	)
	controlpb.RegisterAgentControlServiceServer(server, &grpcControlService{deps: deps})
	controlpb.RegisterAgentBundleServiceServer(server, &grpcBundleService{store: deps.Bundles})
	controlpb.RegisterAgentMetricsServiceServer(server, &grpcMetricsService{deps: deps})
	return server
}
