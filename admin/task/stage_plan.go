package task

import "fmt"

// StageSegment 表示一个 reset 边界划分出的阶段段落。
type StageSegment struct {
	// No 连续 1-based 段落号。
	No int
	// FromStage/ToStage 段落覆盖的配置阶段范围（1-based，含端点）。
	FromStage int
	ToStage   int
	// PeakBots 段落峰值机器人数（段内无 reset，等于段内各阶段增量之和）。
	PeakBots int
	// IsFinal 是否为最后一个段落（其指标来自任务最终报告，而非 reset 中间报告）。
	IsFinal bool
}

// Label 返回段落展示标签，如「第 2 轮 · S3-S4」或「第 1 轮 · S1」。
func (s StageSegment) Label() string {
	if s.FromStage == s.ToStage {
		return fmt.Sprintf("第 %d 轮 · S%d", s.No, s.FromStage)
	}
	return fmt.Sprintf("第 %d 轮 · S%d-S%d", s.No, s.FromStage, s.ToStage)
}

// StagePlan 由 RampUp 配置推导出的阶段段落计划。
type StagePlan struct {
	// HasRampUp 是否配置了渐进式加压。
	HasRampUp bool
	// HasReset 是否包含 reset=true 阶段（决定是否拆分阶段段落）。
	HasReset bool
	// StageCount 配置阶段总数。
	StageCount int
	// Boundaries reset 触发的配置阶段下标（0-based，>=1，按序）。
	// 对应 Agent OnStageReset 上报的 StageIndex。
	Boundaries []int
	// Segments 连续段落列表（仅 HasReset 时有意义；长度 = len(Boundaries)+1）。
	Segments []StageSegment
	// resetIndexToSegmentNo Agent 上报的 reset 配置下标 → 该下标 reset 所结束的段落号。
	resetIndexToSegmentNo map[int]int
}

// SegmentNoForResetIndex 把 Agent 上报的 reset 配置下标映射为连续段落号；不存在返回 0。
func (p StagePlan) SegmentNoForResetIndex(resetIdx int) int {
	return p.resetIndexToSegmentNo[resetIdx]
}

// FinalSegmentNo 返回最终段落号（最后一段，其指标来自任务最终报告）；无 reset 返回 0。
func (p StagePlan) FinalSegmentNo() int {
	if !p.HasReset {
		return 0
	}
	return len(p.Segments)
}

// buildStagePlan 基于 RampUp 配置（理论计划）推导阶段段落。
func BuildStagePlan(cfg *RampUpConfig) StagePlan {
	plan := StagePlan{resetIndexToSegmentNo: map[int]int{}}
	if cfg == nil || len(cfg.Stages) == 0 {
		return plan
	}
	plan.HasRampUp = true
	plan.StageCount = len(cfg.Stages)

	// reset 仅在 i>=1（已有机器人）时触发，i=0 的 reset 被忽略。
	for i := 1; i < len(cfg.Stages); i++ {
		if cfg.Stages[i].Reset {
			plan.Boundaries = append(plan.Boundaries, i)
		}
	}
	if len(plan.Boundaries) == 0 {
		return plan
	}
	plan.HasReset = true

	// 段落范围：seg1 = [0, b1-1]，segK = [b_{k-1}, n-1]。
	prev := 0
	for j, b := range plan.Boundaries {
		seg := StageSegment{
			No:        j + 1,
			FromStage: prev + 1,
			ToStage:   b, // 1-based: 配置下标 b-1 是段末 → 1-based 为 b
			PeakBots:  sumCounts(cfg.Stages, prev, b),
		}
		plan.Segments = append(plan.Segments, seg)
		plan.resetIndexToSegmentNo[b] = j + 1
		prev = b
	}
	// 最后一段：[prev, n-1]，其指标来自任务最终报告。
	plan.Segments = append(plan.Segments, StageSegment{
		No:        len(plan.Boundaries) + 1,
		FromStage: prev + 1,
		ToStage:   len(cfg.Stages),
		PeakBots:  sumCounts(cfg.Stages, prev, len(cfg.Stages)),
		IsFinal:   true,
	})
	return plan
}

// sumCounts 累加 [from, to) 区间（0-based 配置下标）各阶段的增量机器人数。
func sumCounts(stages []RampUpStage, from, to int) int {
	sum := 0
	for i := from; i < to && i < len(stages); i++ {
		sum += stages[i].Count
	}
	return sum
}
