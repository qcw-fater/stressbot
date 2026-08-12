package admin

import (
	"context"
	"errors"
	"io"
	"os"

	"stressbot/controlplane/controlv1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type grpcBundleService struct {
	controlv1.UnimplementedAgentBundleServiceServer
	server *AdminServer
}

func (svc *grpcBundleService) DownloadBundle(request *controlv1.BundleRequest, stream controlv1.AgentBundleService_DownloadBundleServer) error {
	if err := requireAgentID(request.AgentId); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	file, size, release, err := svc.server.bundles.Open(stream.Context(), request.Sha256, request.Offset)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return status.FromContextError(err).Err()
		case errors.Is(err, ErrBundleArgument), errors.Is(err, ErrBundleOffset):
			return status.Error(codes.InvalidArgument, err.Error())
		case errors.Is(err, os.ErrNotExist):
			return status.Error(codes.NotFound, err.Error())
		default:
			return status.Error(codes.Internal, err.Error())
		}
	}
	defer func() { _ = file.Close(); release() }()
	buffer := svc.server.bundles.GetBuffer()
	defer svc.server.bundles.PutBuffer(buffer)
	offset := request.Offset
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			data := append([]byte(nil), buffer[:read]...)
			if err := stream.Send(&controlv1.BundleChunk{Sha256: request.Sha256, TotalSize: size, Offset: offset, Data: data}); err != nil {
				return err
			}
			offset += int64(read)
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return status.Error(codes.Internal, readErr.Error())
		}
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		default:
		}
	}
}
