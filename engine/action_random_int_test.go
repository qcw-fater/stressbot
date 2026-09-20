package engine

import (
	"errors"
	"math"
	"stressbot/binding"
	"strings"
	"testing"

	"stressbot/errcode"
	"stressbot/state"
)

// randomInt/randomFloat 的区间反向是配置错误：旧实现按 min 静默取值，压测跑出来的字段
// 值看着合法，实际没有任何随机性。
func TestResolveRandomRangeInvertedFailsLoud(t *testing.T) {
	ae := &ActionExecutor{store: state.NewStore()}

	for _, tc := range []struct {
		bindType string
		wantText string
	}{
		{binding.BindRandomInt, "randomInt 区间反向"},
		{binding.BindRandomFloat, "randomFloat 区间反向"},
	} {
		t.Run(tc.bindType, func(t *testing.T) {
			_, err := ae.resolveFieldValue(&binding.FieldBind{
				Field: "level", Type: tc.bindType, Min: 10, Max: 3,
			}, "pickLevel")
			if err == nil {
				t.Fatal("min > max 应返回错误")
			}
			actionErr, ok := errors.AsType[*errcode.ActionError](err)
			if !ok || actionErr.Code != errcode.ErrBindField {
				t.Fatalf("err=%v，期望 ErrBindField", err)
			}
			for _, want := range []string{"action=pickLevel", "field=level", tc.wantText} {
				if !strings.Contains(actionErr.Detail, want) {
					t.Errorf("detail=%q 应含 %q", actionErr.Detail, want)
				}
			}
		})
	}
}

// randomInt 的极端区间（宽度超过 int 值域）不能 panic，也不能落到区间外：
// max-min 直接相减会溢出成负数，rand.Intn 收到非正数就 panic，一条坏配置能带崩全部机器人。
func TestResolveRandomIntExtremeRange(t *testing.T) {
	ae := &ActionExecutor{store: state.NewStore()}

	cases := []struct{ min, max int }{
		{math.MinInt, math.MaxInt},
		{math.MinInt, 0},
		{0, math.MaxInt},
		{math.MinInt, math.MinInt + 1},
		{math.MaxInt - 1, math.MaxInt},
		{-5, 5},
		{7, 7},
	}
	for _, tc := range cases {
		for range 64 {
			got, err := ae.resolveFieldValue(&binding.FieldBind{
				Field: "n", Type: binding.BindRandomInt, Min: tc.min, Max: tc.max,
			}, "pickN")
			if err != nil {
				t.Fatalf("min=%d max=%d 不应报错: %v", tc.min, tc.max, err)
			}
			v, ok := got.(int)
			if !ok {
				t.Fatalf("min=%d max=%d 返回 %T，期望 int", tc.min, tc.max, got)
			}
			if v < tc.min || v > tc.max {
				t.Fatalf("min=%d max=%d 取到区间外的 %d", tc.min, tc.max, v)
			}
		}
	}
}

// 心跳 randomInt 与绑定 randomInt 共用同一区间算法，同样不得因宽区间 panic。
func TestHeartbeatRandomIntExtremeRange(t *testing.T) {
	minVal, maxVal := int64(math.MinInt64), int64(math.MaxInt64)
	fields := []HeartbeatField{{Type: "i64", Source: HeartbeatSourceRandomInt, Min: &minVal, Max: &maxVal}}
	for range 64 {
		if _, _, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false); err != nil {
			t.Fatalf("满值域 randomInt 心跳不应报错: %v", err)
		}
	}
}
