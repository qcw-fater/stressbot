package task

import (
	"testing"
)

func newActiveIDTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("创建任务存储失败: %v", err)
	}
	return store
}

func createPendingTask(t *testing.T, store *Store, id string) {
	t.Helper()
	if err := store.Create(&Task{ID: id, Name: id, State: Pending}); err != nil {
		t.Fatalf("创建任务 %s 失败: %v", id, err)
	}
}

// TestBeginRegistersActiveIDAtomically activeID 必须与状态在同一个临界区内变更：
// Begin 返回时任务是 starting，此刻 ActiveTaskID 就该指向它。
func TestBeginRegistersActiveIDAtomically(t *testing.T) {
	store := newActiveIDTestStore(t)
	createPendingTask(t, store, "t1")

	if _, err := store.StartTask("t1"); err != nil {
		t.Fatalf("StartTask 失败: %v", err)
	}
	if got := store.ActiveTaskID(); got != "t1" {
		t.Fatalf("ActiveTaskID = %q，期望 t1", got)
	}
	if !store.HasActive() {
		t.Fatal("HasActive 应为 true")
	}
}

// TestTransitionToTerminalClearsActiveID 进入终态时清除 activeID，之后可以启动新任务。
//
// 回归点：activeID 的登记曾经在 Transition 之外补做，starting→failed 若挤进那段窗口，
// 清除分支会因 activeID 还是空串而漏清，随后补登记把 activeID 永久钉在一个已 failed 的
// 任务上——后续所有启动被 Conflict 挡死，只能重启 Admin。
func TestTransitionToTerminalClearsActiveID(t *testing.T) {
	store := newActiveIDTestStore(t)
	createPendingTask(t, store, "t1")
	createPendingTask(t, store, "t2")

	if _, err := store.StartTask("t1"); err != nil {
		t.Fatalf("StartTask 失败: %v", err)
	}
	if _, err := store.Transition("t1", Starting, Failed); err != nil {
		t.Fatalf("starting→failed 转换失败: %v", err)
	}
	if got := store.ActiveTaskID(); got != "" {
		t.Fatalf("终态后 ActiveTaskID = %q，期望空串", got)
	}

	// 上一个任务已终结，新任务必须能启动，不该被 Conflict 挡住。
	if _, err := store.StartTask("t2"); err != nil {
		t.Fatalf("前一任务已终结后 StartTask 仍失败: %v", err)
	}
	if got := store.ActiveTaskID(); got != "t2" {
		t.Fatalf("ActiveTaskID = %q，期望 t2", got)
	}
}

// TestActiveIDFollowsActiveTransitions 活跃态之间流转时 activeID 保持指向同一任务。
func TestActiveIDFollowsActiveTransitions(t *testing.T) {
	store := newActiveIDTestStore(t)
	createPendingTask(t, store, "t1")

	if _, err := store.StartTask("t1"); err != nil {
		t.Fatalf("StartTask 失败: %v", err)
	}
	for _, step := range []struct{ from, to State }{
		{Starting, Running},
		{Running, Stopping},
	} {
		if _, err := store.Transition("t1", step.from, step.to); err != nil {
			t.Fatalf("%v→%v 转换失败: %v", step.from, step.to, err)
		}
		if got := store.ActiveTaskID(); got != "t1" {
			t.Fatalf("%v 态下 ActiveTaskID = %q，期望 t1", step.to, got)
		}
	}

	if _, err := store.Transition("t1", Stopping, Stopped); err != nil {
		t.Fatalf("stopping→stopped 转换失败: %v", err)
	}
	if got := store.ActiveTaskID(); got != "" {
		t.Fatalf("stopped 后 ActiveTaskID = %q，期望空串", got)
	}
}

// TestTransitionOwnsActiveID activeID 由状态机本身维护，不依赖调用方走哪个入口：
// 任何一条进入活跃态的转换都必须同步登记 activeID。
//
// 这是这次改动的实质。旧实现里 activeID 是 Begin 的记账动作（Transition 返回后补一句
// s.activeID = id），Transition 自己只清不登记；于是 activeID 与任务状态是两笔分开维护的
// 账，两者之间存在窗口——若 starting→failed 挤进去，清除分支会因 activeID 还是空串而漏清，
// 补登记随即把 activeID 钉死在一个已 failed 的任务上，后续所有启动被 Conflict 挡死。
//
// 那个并发窗口极窄（我用 300 轮并发 + race 检测器都没能稳定复现），所以这里不去赌交错，
// 而是直接锁住修复后的结构性质：绕过 Begin 直接转换，activeID 同样要跟上。旧实现下这里
// 会拿到空串。
func TestTransitionOwnsActiveID(t *testing.T) {
	store := newActiveIDTestStore(t)
	createPendingTask(t, store, "t1")

	if _, err := store.Transition("t1", Pending, Starting); err != nil {
		t.Fatalf("pending→starting 转换失败: %v", err)
	}
	if got := store.ActiveTaskID(); got != "t1" {
		t.Fatalf("ActiveTaskID = %q，期望 t1——activeID 必须由 Transition 维护，"+
			"而不是由 Begin 事后补记", got)
	}

	// 既然 activeID 已登记，单活跃约束就自动生效，不依赖启动路径记得去登记。
	createPendingTask(t, store, "t2")
	if _, err := store.StartTask("t2"); err == nil {
		t.Fatal("已有活跃任务时启动第二个应返回 Conflict")
	}
}

// TestBeginRejectsSecondActiveTask 单活跃任务约束：已有活跃任务时启动第二个必须冲突。
func TestBeginRejectsSecondActiveTask(t *testing.T) {
	store := newActiveIDTestStore(t)
	createPendingTask(t, store, "t1")
	createPendingTask(t, store, "t2")

	if _, err := store.StartTask("t1"); err != nil {
		t.Fatalf("StartTask 失败: %v", err)
	}
	if _, err := store.StartTask("t2"); err == nil {
		t.Fatal("已有活跃任务时启动第二个应返回 Conflict")
	}
	if got := store.ActiveTaskID(); got != "t1" {
		t.Fatalf("冲突后 ActiveTaskID = %q，期望仍是 t1", got)
	}
}
