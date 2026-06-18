package robot

import (
	"context"
	"strings"
	"testing"

	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/network"
)

// TestRegisterListen_ResolverNil_FailLoud 验证 T2-C2：listen routeKey 走 resolver，
// Resolve nil（连接 codec 未映射）时 RegisterListen 返回中文 error（fail loud），不静默兜底。
//
// 这是 listen 侧 encode 切到 resolver 的核心契约（brief：listen routeKey
// h.robot.adp.ExpectedRouteKey → h.robot.resolver.Resolve(server).ExpectedRouteKey，
// nil→fail loud）。本用例直接验 fail-loud 路径（refs 第一项的 server 未映射即报错返回）。
func TestRegisterListen_ResolverNil_FailLoud(t *testing.T) {
	// stubResolver 仅映射 tcp:logic；tcp:unknown 未映射 → nil。
	res := &stubResolver{byServer: map[string]adapter.Adapter{
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

	err := h.RegisterListen([]engine.ListenRef{
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
	res := &stubResolver{byServer: map[string]adapter.Adapter{
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

	err := h.RegisterListen([]engine.ListenRef{
		{Server: "tcp:logic", Listen: ""},
	})
	if err != nil {
		t.Fatalf("resolver 命中且无 conn 时 RegisterListen 应返回 nil（跳过注册），got %v", err)
	}
}
