package httpapi

import (
	"testing"
	"time"

	adminagent "stressbot/admin/agent"
)

func TestBuildAgentListItemDropsStaleResourceValues(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.Local)
	cpu := 75.0
	total := uint64(1_000)
	used := uint64(500)
	processCPU := 12.5
	rss := uint64(256)
	agent := &adminagent.AgentNode{
		ID:              "node-a",
		Status:          adminagent.AgentIdle,
		SystemInterval:  "5s",
		SystemUpdatedAt: now.Add(-16 * time.Second),
		LatestSystem: &adminagent.SystemSnapshot{
			HostCPUPercent:    &cpu,
			HostMemTotalBytes: &total,
			HostMemUsedBytes:  &used,
			ProcessCPUPercent: &processCPU,
			ProcessRSSBytes:   &rss,
			ProcessGoroutines: 9,
		},
	}

	got := buildAgentListItem(agent, now)
	if !got.SystemStale {
		t.Fatal("过期资源快照必须明确标记为 stale")
	}
	if got.SystemSnapshotAgeSeconds == nil || *got.SystemSnapshotAgeSeconds != 16 {
		t.Fatalf("snapshot age = %v, want 16", got.SystemSnapshotAgeSeconds)
	}
	if got.HostCPUPercent != nil || got.HostMemPercent != nil || got.ProcessCPUPercent != nil || got.ProcessRSSBytes != nil || got.ProcessGoroutines != nil {
		t.Fatal("过期资源快照不得继续向节点列表暴露数值")
	}
}

func TestBuildAgentListItemUsesValidatedFreshResourceValues(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.Local)
	cpu := 25.0
	total := uint64(4_000)
	used := uint64(1_000)
	wrongPercent := 99.0
	agent := &adminagent.AgentNode{
		ID:              "node-a",
		Status:          adminagent.AgentBusy,
		SystemInterval:  "5s",
		SystemUpdatedAt: now.Add(-10 * time.Second),
		LatestSystem: &adminagent.SystemSnapshot{
			HostCPUPercent:    &cpu,
			HostMemTotalBytes: &total,
			HostMemUsedBytes:  &used,
			HostMemPercent:    &wrongPercent,
		},
	}

	got := buildAgentListItem(agent, now)
	if got.SystemStale {
		t.Fatal("有效期内的在线节点资源快照不应标记为 stale")
	}
	if got.HostCPUPercent == nil || *got.HostCPUPercent != 25 {
		t.Fatalf("host CPU = %v, want 25", got.HostCPUPercent)
	}
	if got.HostMemPercent == nil || *got.HostMemPercent != 25 {
		t.Fatalf("host memory = %v, want recomputed 25", got.HostMemPercent)
	}
}
