package admin

import (
	"io"
	"stressbot/controlplane/controlv1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type grpcTelemetryService struct {
	controlv1.UnimplementedAgentTelemetryServiceServer
	server *AdminServer
}

func (svc *grpcTelemetryService) Report(stream controlv1.AgentTelemetryService_ReportServer) error {
	var accepted, rejected uint64
	var agentID string
	for {
		envelope, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&controlv1.TelemetryClose{Accepted: accepted, Rejected: rejected})
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
			return status.Error(codes.Aborted, "遥测会话已过期")
		}
		if !svc.server.sessions.Current(agentID, envelope.Generation) {
			return status.Error(codes.Aborted, "遥测会话已过期")
		}
		if previousErr := svc.server.telemetry.TakeError(agentID, envelope.Generation); previousErr != nil {
			return status.Error(codes.InvalidArgument, previousErr.Error())
		}
		if err := svc.server.telemetry.Offer(envelope); err != nil {
			rejected++
			return status.Error(codes.ResourceExhausted, err.Error())
		}
		accepted++
	}
}
