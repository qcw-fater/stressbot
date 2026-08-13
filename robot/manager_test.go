package robot

import (
	"context"
	"errors"
	"testing"
)

// --- B1 ramp-up 生命周期计数（active/generation/creationDone → doneCh）单元测试 ---
//
// 这些测试直接驱动 Manager 的记账原语（不创建真实 Robot：NewManager 不解引用其依赖，
// onRobotDone/finishCreation/generation 逻辑也不触碰 Robot 内部，用空 *Robot 作身份即可），
// 覆盖：错过完成事件（永不结束）、关闭竞态两序、阶段间瞬时归零（提前结束）、旧代回调隔离。

func newBookkeepManager() *Manager {
	return NewManager(context.Background(), ManagerConfig{}, nil, nil, nil, nil)
}

func TestManagerParentCancellationInterruptsRampUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := NewManager(ctx, ManagerConfig{
		RampUp: &RampUpConfig{Stages: []RampUpStage{{Count: 0}, {Count: 0}}},
	}, nil, nil, nil, nil)
	m.OnStageChange = func(current, _ int) {
		if current == 1 {
			cancel()
		}
	}

	err := m.StartWithRampUp()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StartWithRampUp() error = %v, want context.Canceled", err)
	}
	if !doneClosed(m) {
		t.Fatal("取消 ramp-up 后应结束创建阶段并关闭 Done")
	}
}

func doneClosed(m *Manager) bool {
	select {
	case <-m.doneCh:
		return true
	default:
		return false
	}
}

// simCreate 模拟 startBatch 创建 n 个当前代机器人：登记 active + 入 robots，返回其指针与所属代号。
func simCreate(m *Manager, n int) ([]*Robot, int32) {
	gen := m.generation.Load()
	rs := make([]*Robot, n)
	for i := range rs {
		r := &Robot{}
		m.mu.Lock()
		m.robots = append(m.robots, r)
		m.robotIdx[r] = len(m.robots) - 1
		m.mu.Unlock()
		m.active.Add(1)
		rs[i] = r
	}
	return rs, gen
}

// 缺陷1：秒退机器人在 finishCreation 之前回调，最后一个完成后仍能关闭 doneCh（不永久卡死）。
func TestManagerDoneClosesAfterDrain(t *testing.T) {
	m := newBookkeepManager()
	rs, gen := simCreate(m, 3)

	m.onRobotDone(gen, rs[0], CleanupStatus{})
	m.onRobotDone(gen, rs[1], CleanupStatus{})
	if doneClosed(m) {
		t.Fatal("创建阶段结束前不应关闭 doneCh")
	}

	m.finishCreation() // 仍有 1 个存活，不应关闭
	if doneClosed(m) {
		t.Fatal("仍有存活机器人时不应关闭 doneCh")
	}

	m.onRobotDone(gen, rs[2], CleanupStatus{}) // active=0 且 creationDone → 关闭
	if !doneClosed(m) {
		t.Fatal("最后一个机器人完成后应关闭 doneCh")
	}
}

// 关闭竞态另一序：finishCreation 先于最后一个 onRobotDone，也应正确关闭。
func TestManagerDoneClosesWhenFinishBeforeLastDone(t *testing.T) {
	m := newBookkeepManager()
	rs, gen := simCreate(m, 1)

	m.finishCreation() // active=1 → 不关
	if doneClosed(m) {
		t.Fatal("仍有存活机器人时不应关闭 doneCh")
	}
	m.onRobotDone(gen, rs[0], CleanupStatus{}) // active=0 且 creationDone → 关
	if !doneClosed(m) {
		t.Fatal("最后一个完成后应关闭 doneCh")
	}
}

// 提前关：阶段间机器人瞬时全部归零，但创建阶段未结束，不得提前关闭 doneCh。
func TestManagerDoesNotCloseOnTransientZeroDuringRampUp(t *testing.T) {
	m := newBookkeepManager()

	rs1, gen := simCreate(m, 2) // 阶段一
	m.onRobotDone(gen, rs1[0], CleanupStatus{})
	m.onRobotDone(gen, rs1[1], CleanupStatus{}) // active=0，但 creationDone=false
	if doneClosed(m) {
		t.Fatal("创建阶段未结束时阶段间归零不应关闭 doneCh")
	}

	rs2, gen2 := simCreate(m, 2) // 阶段二（无 reset，同代）
	m.finishCreation()
	m.onRobotDone(gen2, rs2[0], CleanupStatus{})
	if doneClosed(m) {
		t.Fatal("仍有存活机器人时不应关闭 doneCh")
	}
	m.onRobotDone(gen2, rs2[1], CleanupStatus{})
	if !doneClosed(m) {
		t.Fatal("全部机器人完成后应关闭 doneCh")
	}
}

// 缺陷2：resetBots 递增代号后，旧代机器人的迟到回调必须被隔离，不污染新代 active / 不误关 doneCh。
func TestManagerGenerationIsolatesStaleCallbacks(t *testing.T) {
	m := newBookkeepManager()

	rs1, gen0 := simCreate(m, 2)
	// 模拟 resetBots 的核心：先递增代号，再清零 active。
	m.generation.Add(1)
	m.active.Store(0)

	// 旧代迟到回调：应被 gen 检查挡下，不改 active、不关 doneCh。
	m.onRobotDone(gen0, rs1[0], CleanupStatus{})
	m.onRobotDone(gen0, rs1[1], CleanupStatus{})
	if m.active.Load() != 0 {
		t.Fatalf("旧代回调污染了 active：%d，期望 0", m.active.Load())
	}
	if doneClosed(m) {
		t.Fatal("旧代回调不应关闭 doneCh")
	}

	// 新代机器人。
	rs2, gen1 := simCreate(m, 2)
	if gen1 != 1 {
		t.Fatalf("新代代号 = %d，期望 1", gen1)
	}
	m.finishCreation()

	// 再来一个旧代 straggler：仍应被忽略（且其摘除循环不会误删新代机器人）。
	m.onRobotDone(gen0, rs1[0], CleanupStatus{})
	if doneClosed(m) {
		t.Fatal("旧代 straggler 不应关闭 doneCh")
	}

	m.onRobotDone(gen1, rs2[0], CleanupStatus{})
	m.onRobotDone(gen1, rs2[1], CleanupStatus{})
	if !doneClosed(m) {
		t.Fatal("新代全部完成后应关闭 doneCh")
	}
	if m.active.Load() != 0 {
		t.Fatalf("最终 active = %d，期望 0", m.active.Load())
	}
}

func TestManagerRobotIdentityUsesTaskGlobalIndex(t *testing.T) {
	cfg := ManagerConfig{
		AccountPrefix: "bot_",
		StartNumber:   100,
		StartIndex:    6,
	}

	id, index, account := cfg.robotIdentity(0)
	if index != 6 {
		t.Errorf("index = %d, want 6", index)
	}
	if id != 106 {
		t.Errorf("id = %d, want 106", id)
	}
	if account != "bot_106" {
		t.Errorf("account = %q, want %q", account, "bot_106")
	}
}

func TestManagerRobotIdentityKeepsGlobalIndexAcrossBatches(t *testing.T) {
	cfg := ManagerConfig{
		AccountPrefix: "bot_",
		StartNumber:   20,
		StartIndex:    40,
	}

	id, index, account := cfg.robotIdentity(7)
	if index != 47 {
		t.Errorf("index = %d, want 47", index)
	}
	if id != 67 {
		t.Errorf("id = %d, want 67", id)
	}
	if account != "bot_67" {
		t.Errorf("account = %q, want %q", account, "bot_67")
	}
}
