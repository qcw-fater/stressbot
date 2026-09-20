package robot

import (
	"context"
	flowdef "stressbot/flow"
	"strings"
	"testing"

	"stressbot/network"
	"stressbot/protocol"
)

// TestRegisterListen_ResolverNil_FailLoud 验证 T2-C2：listen routeKey 走 resolver，
// Resolve nil（连接 codec 未映射）时 RegisterListen 返回中文 error（fail loud），不静默兜底。
//
// 这是 listen 侧 encode 切到 resolver 的核心契约（brief：listen routeKey
// h.robot.adp.ExpectedRouteKey → h.robot.resolver.Resolve(server).ExpectedRouteKey，
// nil→fail loud）。本用例直接验 fail-loud 路径（refs 第一项的 server 未映射即报错返回）。
func TestRegisterListen_ResolverNil_FailLoud(t *testing.T) {
	// stubResolver 仅映射 tcp:logic；tcp:unknown 未映射 → nil。
	res := &stubResolver{byServer: map[string]protocol.Adapter{
		"tcp:logic": fakeAdapter{},
	}}
	r := &Robot{
		id:       1,
		account:  "bot_test",
		ctx:      context.Background(),
		cancel:   func() {},
		resolver: res,
		client:   network.NewClient("bot_test", 0, ""),
	}
	h := &robotActionHandler{robot: r, flow: nil}

	err := h.RegisterListen([]flowdef.ListenRef{
		{Server: "tcp:unknown", Listen: "L"},
	})
	if err == nil {
		t.Fatalf("Resolve nil 时 RegisterListen 应返回 error（fail loud）")
	}
	if !strings.Contains(err.Error(), "codec") {
		t.Fatalf("error %q 应含 codec 子串", err.Error())
	}
	if !strings.Contains(err.Error(), "tcp:unknown") {
		t.Fatalf("error %q 应含 server 串 tcp:unknown", err.Error())
	}
}

// TestRegisterListen_ResolverHit_NoError 验证 resolver 命中时 RegisterListen 不报 codec 错误
// （走到连接查找阶段，因未建立连接 GetTCPConn 返回 nil → Debug 跳过，最终返回 nil）。
func TestRegisterListen_ResolverHit_NoError(t *testing.T) {
	res := &stubResolver{byServer: map[string]protocol.Adapter{
		"tcp:logic": fakeAdapter{},
	}}
	// 真实空 client（无连接）→ GetTCPConn 返回 nil → 跳过该 group，不报错。
	r := &Robot{
		id:       2,
		account:  "bot_test2",
		ctx:      context.Background(),
		cancel:   func() {},
		resolver: res,
		client:   network.NewClient("bot_test2", 0, ""),
	}
	h := &robotActionHandler{robot: r, flow: nil}

	err := h.RegisterListen([]flowdef.ListenRef{
		{Server: "tcp:logic", Listen: ""},
	})
	if err != nil {
		t.Fatalf("resolver 命中且无 conn 时 RegisterListen 应返回 nil（跳过注册），got %v", err)
	}
}

// TestRegisterListen_MissingListenDef_FailLoud 守护：listenRefs[].listen 引用的 listens
// 项缺失时，RegisterListen 必须返回 error。
//
// 该契约在本轮从「Error 日志 + 跳过该条」改成 fail loud。原行为的代价是流程照跑不误、
// 只在日志里留一行，而这条监听永远不会触发——压测跑完才发现推送一条没收到。改成 fail
// loud 之后，正常配置路径其实到不了这里：同一笔误已由 flow.PrepareTaskFlow 的跨表引用
// 校验在加载期挡掉（见 flow/prepare.go）。这里保留运行期硬失败作为第二道防线，覆盖绕过
// PrepareTaskFlow 直接构造 TaskFlow 的调用方。
func TestRegisterListen_MissingListenDef_FailLoud(t *testing.T) {
	res := &stubResolver{byServer: map[string]protocol.Adapter{
		"tcp:logic": fakeAdapter{},
	}}
	r := &Robot{
		id:       3,
		account:  "bot_test3",
		ctx:      context.Background(),
		cancel:   func() {},
		resolver: res,
		client:   network.NewClient("bot_test3", 0, ""),
	}
	h := &robotActionHandler{robot: r, flow: &flowdef.TaskFlow{Listens: map[string]*flowdef.ListenDef{}}}

	err := h.RegisterListen([]flowdef.ListenRef{
		{Server: "tcp:logic", Listen: "missing", Route: map[string]any{"cmd": 1, "act": 2}},
	})
	if err == nil {
		t.Fatal("缺失 listen 定义时 RegisterListen 应返回 error")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("错误信息应点名缺失的 listen，got %v", err)
	}
}
