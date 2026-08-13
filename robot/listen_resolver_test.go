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

// TestRegisterListen_MissingListenDef_NoHardFail 守护：listenRefs[].listen 显式引用的
// listens 项缺失时，RegisterListen 不得在阶段内 hard-fail（返回 error）。
//
// 注意：本断言仅锁定「不 hard-fail」这一点，不区分「跳过该条」与「按 nil 回调注册」。
// 这两种行为在本测试的脚手架下不可观测——network.NewClient 未建立连接，GetTCPConn 返回
// nil 导致整组被跳过，无论 entry 是否被加入 groups 都观察不到副作用；为可观测化而注入
// 真实连接或测试专用钩子会超出既有 stubResolver/fakeAdapter 测试风格，得不偿失。
// 因此「缺失 listen 定义 → 跳过该条（Error 日志 + continue）」与「配置笔误被静默注册成
// nil 回调队列」的区分由 code review 守护，而非本断言。本断言只防止「配置笔误直接
// hard-fail 中断整个流程」这一回归。
func TestRegisterListen_MissingListenDef_NoHardFail(t *testing.T) {
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
	if err != nil {
		t.Fatalf("缺失 listen 定义不应在 RegisterListen hard-fail，got %v", err)
	}
}
