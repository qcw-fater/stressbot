package grpcapi

import (
	"testing"

	admintask "stressbot/admin/task"
)

func validDispatch() admintask.Dispatch {
	return admintask.Dispatch{
		TaskID: "task-1", TaskName: "压测", StartIndex: 0, TotalBots: 10, ConcurrentNum: 2,
		TCPTimeout: "60s", HTTPTimeout: "10s",
	}
}

// TaskAssignmentToProto 的 DefaultDelayMs 转换契约：0 = 引擎默认 1000ms；
// 非零值（含 -1 边界）原样转 int32。
func TestTaskAssignmentToProtoDefaultDelayMs(t *testing.T) {
	t.Run("零值保持零", func(t *testing.T) {
		out, err := TaskAssignmentToProto(validDispatch())
		if err != nil {
			t.Fatalf("TaskAssignmentToProto() error = %v", err)
		}
		if out.DefaultDelayMs != 0 {
			t.Fatalf("DefaultDelayMs = %d, want 0", out.DefaultDelayMs)
		}
	})

	t.Run("非零值原样转换", func(t *testing.T) {
		for _, want := range []int{-1, 500, 600000} {
			src := validDispatch()
			src.DefaultDelayMs = want
			out, err := TaskAssignmentToProto(src)
			if err != nil {
				t.Fatalf("TaskAssignmentToProto() error = %v", err)
			}
			if out.DefaultDelayMs != int32(want) {
				t.Fatalf("DefaultDelayMs = %d, want %d", out.DefaultDelayMs, want)
			}
		}
	})
}
