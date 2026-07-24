package admin

import (
	"strings"
	"testing"
	"time"

	json "stressbot/utils/jsonx"
)

func TestComputeFlowSnapshotRevisionIsOrderIndependent(t *testing.T) {
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	a := FlowTemplateDetail{
		FlowTemplateSummary: FlowTemplateSummary{ID: "a", Name: "A", CreatedAt: now, UpdatedAt: now},
		Flow:                json.RawMessage(`{"defaultDelayMs":1000,"nodes":{},"actions":{},"listens":{}}`),
	}
	b := FlowTemplateDetail{
		FlowTemplateSummary: FlowTemplateSummary{ID: "b", Name: "B", CreatedAt: now, UpdatedAt: now},
		Flow:                json.RawMessage(`{"defaultDelayMs":1000,"nodes":{},"actions":{},"listens":{}}`),
	}

	r1, err := computeFlowSnapshotRevision([]FlowTemplateDetail{a, b})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := computeFlowSnapshotRevision([]FlowTemplateDetail{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if r1 != r2 {
		t.Fatalf("revision differs: %q != %q", r1, r2)
	}
}

func TestValidateFlowSnapshotItemsRejectsDuplicateID(t *testing.T) {
	items := []FlowTemplateDetail{
		{
			FlowTemplateSummary: FlowTemplateSummary{ID: "same", Name: "A"},
			Flow:                json.RawMessage(`{"nodes":{},"actions":{}}`),
		},
		{
			FlowTemplateSummary: FlowTemplateSummary{ID: "same", Name: "B"},
			Flow:                json.RawMessage(`{"nodes":{},"actions":{}}`),
		},
	}

	err := validateFlowSnapshotItems(items)
	if err == nil || !strings.Contains(err.Error(), "流程 ID 重复") {
		t.Fatalf("error = %v", err)
	}
}
