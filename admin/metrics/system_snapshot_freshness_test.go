package metrics

import (
	"testing"
	"time"
)

func TestUpdateSystemDoesNotRefreshDuplicateSnapshotSequence(t *testing.T) {
	t0 := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	registry := newTestAgentRegistry(
		&Node{
			ID:              "a",
			LatestSystem:    &SystemSnapshot{Sequence: 5},
			SystemUpdatedAt: t0,
		},
	)

	registry.UpdateSystem("a", &SystemSnapshot{Sequence: 5}, t1)
	duplicate, _ := registry.Get("a")
	if !duplicate.SystemUpdatedAt.Equal(t0) || duplicate.LatestSystem.Sequence != 5 {
		t.Fatalf("duplicate snapshot refreshed state: sequence=%d updatedAt=%v", duplicate.LatestSystem.Sequence, duplicate.SystemUpdatedAt)
	}

	registry.UpdateSystem("a", &SystemSnapshot{Sequence: 6}, t1)
	advanced, _ := registry.Get("a")
	if !advanced.SystemUpdatedAt.Equal(t1) || advanced.LatestSystem.Sequence != 6 {
		t.Fatalf("advanced snapshot was not accepted: sequence=%d updatedAt=%v", advanced.LatestSystem.Sequence, advanced.SystemUpdatedAt)
	}
}
