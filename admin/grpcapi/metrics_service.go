package grpcapi

import (
	"errors"
	"io"

	controlpb "stressbot/controlplane/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type grpcMetricsService struct {
	controlpb.UnimplementedAgentMetricsServiceServer
	deps Dependencies
}

func (svc *grpcMetricsService) Report(stream controlpb.AgentMetricsService_ReportServer) error {
	var accepted, rejected uint64
	var agentID string
	for {
		envelope, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(&controlpb.MetricsClose{Accepted: accepted, Rejected: rejected})
		}
		if err != nil {
			return err
		}
		if agentID == "" {
			agentID = envelope.AgentId
			if err := requireAgentID(agentID); err != nil {
				return status.Error(codes.InvalidArgument, err.Error())
			}
		}
		if envelope.AgentId != agentID {
			return status.Error(codes.Aborted, "指标会话已过期")
		}
		if !svc.deps.Sessions.Current(agentID, envelope.Generation) {
			return status.Error(codes.Aborted, "指标会话已过期")
		}
		if previousErr := svc.deps.Metrics.TakeError(agentID, envelope.Generation); previousErr != nil {
			return status.Error(codes.InvalidArgument, previousErr.Error())
		}
		if err := svc.deps.Metrics.Offer(envelope); err != nil {
			return status.Error(codes.ResourceExhausted, err.Error())
		}
		accepted++
	}
}
