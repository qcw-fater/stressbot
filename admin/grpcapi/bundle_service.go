package grpcapi

import (
	"context"
	"errors"
	"io"
	"os"

	"stressbot/admin/bundle"
	"stressbot/controlplane/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type grpcBundleService struct {
	controlpb.UnimplementedAgentBundleServiceServer
	store *bundle.Store
}

func (svc *grpcBundleService) DownloadBundle(request *controlpb.BundleRequest, stream controlpb.AgentBundleService_DownloadBundleServer) error {
	if err := requireAgentID(request.AgentId); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	file, size, release, err := svc.store.Open(stream.Context(), request.Sha256, request.Offset)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return status.FromContextError(err).Err()
		case errors.Is(err, bundle.ErrBundleArgument), errors.Is(err, bundle.ErrBundleOffset):
			return status.Error(codes.InvalidArgument, err.Error())
		case errors.Is(err, os.ErrNotExist):
			return status.Error(codes.NotFound, err.Error())
		default:
			return status.Error(codes.Internal, err.Error())
		}
	}
	defer func() { _ = file.Close(); release() }()
	buffer := svc.store.GetBuffer()
	defer svc.store.PutBuffer(buffer)
	offset := request.Offset
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			data := append([]byte(nil), buffer[:read]...)
			if err := stream.Send(&controlpb.BundleChunk{Sha256: request.Sha256, TotalSize: size, Offset: offset, Data: data}); err != nil {
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
