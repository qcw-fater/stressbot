package script

import (
	"context"
	"testing"
)

// 身份键 id/index/account 由框架按机器人身份写入，robot.clear()/robot.delete() 不得清除，
// 与 stateExtra 覆盖保护、clearState 动作校验同一口径。
func TestRobotClearKeepsIdentityKeys(t *testing.T) {
	L := newTestState(context.Background(), t, &fakeNetSender{}, nil)
	defer L.Close()
	ctx := GetContext(L)

	ctx.Store.Set("id", 1)
	ctx.Store.Set("index", 0)
	ctx.Store.Set("account", "bot_1")
	ctx.Store.Set("gold", 100)
	ctx.Store.Set("heroes", "alice")

	if err := L.DoString(`
		local robot = require("robot")
		robot.clear()
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	for _, key := range []string{"id", "index", "account"} {
		if !ctx.Store.Has(key) {
			t.Fatalf("robot.clear() 清除了框架保留键 %s", key)
		}
	}
	for _, key := range []string{"gold", "heroes"} {
		if ctx.Store.Has(key) {
			t.Fatalf("robot.clear() 未清除普通键 %s", key)
		}
	}

	if err := L.DoString(`require("robot").delete("id")`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	if !ctx.Store.Has("id") {
		t.Fatal("robot.delete() 清除了框架保留键 id")
	}

	if err := L.DoString(`require("robot").clear("index")`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	if !ctx.Store.Has("index") {
		t.Fatal("robot.clear(key) 清除了框架保留键 index")
	}

	ctx.Store.Set("gold", 200)
	if err := L.DoString(`require("robot").delete("gold")`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	if ctx.Store.Has("gold") {
		t.Fatal("robot.delete() 未清除普通键 gold")
	}
}
