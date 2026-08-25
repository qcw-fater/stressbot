// Package history 将终态压测任务归档到 MySQL，并提供历史任务列表、配置快照回读与指标时间序列查询。
package history

// 时序采样点数策略：DefaultTimeseriesMaxPoints 是趋势查询默认返回的采样点数上限，
// MaxTimeseriesMaxPoints 是允许调用方通过 maxPoints 参数指定的硬上界，超出即被截断。
const (
	DefaultTimeseriesMaxPoints = 600
	MaxTimeseriesMaxPoints     = 2000
)
