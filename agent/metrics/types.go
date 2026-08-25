package metrics

import (
	"time"

	controlpb "stressbot/controlplane/pb"
)

// SystemSnapshot 是单次系统资源采集快照。
type SystemSnapshot struct {
	Timestamp time.Time `json:"timestamp"`
	Sequence  uint64    `json:"sequence"`

	HostCPUPercent         *float64 `json:"hostCpuPercent"`
	HostMemTotalBytes      *uint64  `json:"hostMemTotalBytes"`
	HostMemUsedBytes       *uint64  `json:"hostMemUsedBytes"`
	HostMemPercent         *float64 `json:"hostMemPercent"`
	HostNetSendBytesPerSec *float64 `json:"hostNetSendBytesPerSec"`
	HostNetRecvBytesPerSec *float64 `json:"hostNetRecvBytesPerSec"`
	ProcessCPUPercent      *float64 `json:"processCpuPercent"`
	ProcessRSSBytes        *uint64  `json:"processRssBytes"`
	ProcessHeapBytes       uint64   `json:"processHeapBytes"`
	ProcessGoroutines      int      `json:"processGoroutines"`
	ProcessThreads         *int32   `json:"processThreads"`
	ProcessFDs             *int32   `json:"processFds"`
}

// StaticInfo 是节点启动时采集一次的静态信息。
type StaticInfo struct {
	Hostname      string    `json:"hostname"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	NumCPU        int       `json:"numCpu"`
	MemTotalBytes uint64    `json:"memTotalBytes"`
	GoVersion     string    `json:"goVersion"`
	KernelVer     string    `json:"kernelVer"`
	StartedAt     time.Time `json:"startedAt"`
}

// StaticInfoToProto 转换控制面静态信息。
func StaticInfoToProto(src StaticInfo) *controlpb.StaticInfo {
	return &controlpb.StaticInfo{
		Hostname:          src.Hostname,
		Os:                src.OS,
		Arch:              src.Arch,
		CpuCount:          int32(src.NumCPU),
		MemoryTotalBytes:  src.MemTotalBytes,
		GoVersion:         src.GoVersion,
		KernelVersion:     src.KernelVer,
		StartedAtUnixNano: src.StartedAt.UnixNano(),
	}
}

// SystemSnapshotToProto 转换控制面系统指标快照。
func SystemSnapshotToProto(src SystemSnapshot) *controlpb.SystemMetricSnapshot {
	return &controlpb.SystemMetricSnapshot{
		TimestampUnixNano:                src.Timestamp.UnixNano(),
		Sequence:                         src.Sequence,
		HostCpuPercent:                   cloneFloat64(src.HostCPUPercent),
		HostMemoryTotalBytes:             cloneUint64(src.HostMemTotalBytes),
		HostMemoryUsedBytes:              cloneUint64(src.HostMemUsedBytes),
		HostMemoryPercent:                cloneFloat64(src.HostMemPercent),
		HostNetworkSendBytesPerSecond:    cloneFloat64(src.HostNetSendBytesPerSec),
		HostNetworkReceiveBytesPerSecond: cloneFloat64(src.HostNetRecvBytesPerSec),
		ProcessCpuPercent:                cloneFloat64(src.ProcessCPUPercent),
		ProcessRssBytes:                  cloneUint64(src.ProcessRSSBytes),
		ProcessHeapBytes:                 src.ProcessHeapBytes,
		ProcessGoroutines:                int32(src.ProcessGoroutines),
		ProcessThreads:                   cloneInt32(src.ProcessThreads),
		ProcessFileDescriptors:           cloneInt32(src.ProcessFDs),
	}
}

func cloneFloat64(src *float64) *float64 {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}

func cloneUint64(src *uint64) *uint64 {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}

func cloneInt32(src *int32) *int32 {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}
