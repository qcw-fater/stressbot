package admin

import (
	"time"

	"stressbot/controlplane/controlv1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

const maxControlMessageSize = 16 << 20

func (s *AdminServer) newGRPCServer() *grpc.Server {
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
	controlv1.RegisterAgentControlServiceServer(server, &grpcControlService{server: s})
	controlv1.RegisterAgentBundleServiceServer(server, &grpcBundleService{server: s})
	controlv1.RegisterAgentTelemetryServiceServer(server, &grpcTelemetryService{server: s})
	return server
}

func (s *AdminServer) heartbeatInterval() time.Duration {
	duration, err := time.ParseDuration(s.cfg.ControlPlane.HeartbeatInterval)
	if err != nil || duration <= 0 {
		return 10 * time.Second
	}
	return duration
}

func (s *AdminServer) leaseDuration() time.Duration {
	duration, err := time.ParseDuration(s.cfg.ControlPlane.UnhealthyAfter)
	if err != nil || duration <= 0 {
		return 30 * time.Second
	}
	return duration
}
