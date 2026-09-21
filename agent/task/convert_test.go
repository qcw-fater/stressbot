package task

import (
	"testing"

	controlpb "stressbot/controlplane/pb"
)

func validAssignmentProto() *controlpb.TaskAssignment {
	return &controlpb.TaskAssignment{
		TaskId:        "task-1",
		StartIndex:    0,
		TotalBots:     10,
		ConcurrentNum: 2,
	}
}

func validBundleProto() *controlpb.BundleDescriptor {
	return &controlpb.BundleDescriptor{
		Sha256: make([]byte, 32),
		Size:   1024,
	}
}

// DefaultDelayMs 契约：Admin 下发 0（proto 缺省）= 引擎默认 1000ms；
// 非零值（含 -1 边界）原样传递。
func TestFromProtoDefaultDelayMs(t *testing.T) {
	t.Run("零值保持零", func(t *testing.T) {
		out, err := FromProto(validAssignmentProto(), validBundleProto())
		if err != nil {
			t.Fatalf("FromProto() error = %v", err)
		}
		if out.DefaultDelayMs != 0 {
			t.Fatalf("DefaultDelayMs = %d, want 0", out.DefaultDelayMs)
		}
	})

	t.Run("非零值原样传递", func(t *testing.T) {
		for _, want := range []int32{-1, 500, 600000} {
			src := validAssignmentProto()
			src.DefaultDelayMs = want
			out, err := FromProto(src, validBundleProto())
			if err != nil {
				t.Fatalf("FromProto() error = %v", err)
			}
			if out.DefaultDelayMs != int(want) {
				t.Fatalf("DefaultDelayMs = %d, want %d", out.DefaultDelayMs, want)
			}
		}
	})
}
