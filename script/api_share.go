package script

import (
	"context"
	"time"

	"stressbot/sharedstate"

	lua "github.com/yuin/gopher-lua"
)

// loadShareModule 加载 share 命名空间模块（Redis-backed 跨 Robot/Agent 共享状态）。
//
// Lua 用法：
//
//	local share = require("share")
//
// 返回值约定：
//   - 写入/判定类（set/del/exists/expire/incr/claim/...）返回 `(value_or_ok, err)`。
//   - 读取类（get/hash_get/queue_pop）返回 `(value, ok, err)`：ok=false 表示「不存在/空队列」，
//     用于和「存储的 JSON null」区分。
//   - err == nil 表示调用成功；err ~= nil 为字符串错误。
//   - 未启用共享状态（任务未使用 Redis）时返回明确错误，不中断脚本。
//
// 命名空间语义：每种数据类型（kv/counter/queue/hash/claim）有独立命名空间。
//   - del / exists / expire 作用于「数据类型」全集（kv+counter+queue+hash），不含 claim
//     （锁请用 release/renew）。即 share.del(key) 会清掉该 key 名下的所有数据形态。
//
// 阻塞型 Redis 调用直接阻塞当前 Robot 主流程 goroutine；操作上下文继承
// Robot 生命周期 ctx，并受共享状态 OpTimeout 限制。
func loadShareModule(L *lua.LState) int {
	mod := L.NewTable()

	// KV
	L.SetField(mod, "set", L.NewFunction(shareSet))
	L.SetField(mod, "get", L.NewFunction(shareGet))
	L.SetField(mod, "del", L.NewFunction(shareDel))
	L.SetField(mod, "exists", L.NewFunction(shareExists))
	L.SetField(mod, "expire", L.NewFunction(shareExpire))
	// Counter
	L.SetField(mod, "incr", L.NewFunction(shareIncr))
	// Claim
	L.SetField(mod, "claim", L.NewFunction(shareClaim))
	L.SetField(mod, "release", L.NewFunction(shareRelease))
	L.SetField(mod, "owner", L.NewFunction(shareOwner))
	L.SetField(mod, "renew", L.NewFunction(shareRenew))
	// Queue
	L.SetField(mod, "queue_push", L.NewFunction(shareQueuePush))
	L.SetField(mod, "queue_pop", L.NewFunction(shareQueuePop))
	L.SetField(mod, "queue_len", L.NewFunction(shareQueueLen))
	L.SetField(mod, "queue_expire", L.NewFunction(shareQueueExpire))
	// Hash
	L.SetField(mod, "hash_set", L.NewFunction(shareHashSet))
	L.SetField(mod, "hash_get", L.NewFunction(shareHashGet))
	L.SetField(mod, "hash_get_all", L.NewFunction(shareHashGetAll))
	L.SetField(mod, "hash_del", L.NewFunction(shareHashDel))
	L.SetField(mod, "hash_incr", L.NewFunction(shareHashIncr))
	L.SetField(mod, "hash_expire", L.NewFunction(shareHashExpire))

	L.Push(mod)
	return 1
}

// ── 内部辅助 ──────────────────────────────────────────

// shareCtx 校验共享状态已启用并返回 Context；未启用时返回 nil。
func shareCtx(L *lua.LState) *Context {
	ctx := GetContext(L)
	if ctx == nil || ctx.Shared == nil {
		return nil
	}
	return ctx
}

// pushNotEnabled 推送「未启用共享状态」错误（first, errString）。
func pushNotEnabled(L *lua.LState, first lua.LValue) int {
	L.Push(first)
	L.Push(lua.LString(sharedstate.ErrNotEnabled.Error()))
	return 2
}

// pushShareResult 推送 (first, err)；err 为 nil 时第二返回值为 nil。
func pushShareResult(L *lua.LState, first lua.LValue, err error) int {
	L.Push(first)
	if err != nil {
		L.Push(lua.LString(err.Error()))
	} else {
		L.Push(lua.LNil)
	}
	return 2
}

// pushResult3 推送 (value, ok, err)；供读取类 API 区分「不存在」与「存储了 null」。
func pushResult3(L *lua.LState, value lua.LValue, ok bool, err error) int {
	L.Push(value)
	L.Push(lua.LBool(ok))
	if err != nil {
		L.Push(lua.LString(err.Error()))
	} else {
		L.Push(lua.LNil)
	}
	return 3
}

// pushNotEnabled3 「未启用共享状态」的三值返回 (nil, false, err)。
func pushNotEnabled3(L *lua.LState) int {
	return pushResult3(L, lua.LNil, false, sharedstate.ErrNotEnabled)
}

// opContext 基于 robot 生命周期 ctx + OpTimeout 派生操作上下文。
func opContext(ctx *Context) (context.Context, context.CancelFunc) {
	base := ctx.Ctx
	if base == nil {
		base = context.Background()
	}
	to := ctx.Shared.OpTimeout()
	if to <= 0 {
		return context.WithCancel(base)
	}
	return context.WithTimeout(base, to)
}

// optTTL 读取栈位 idx 的可选 ttlSec 参数（秒），缺省返回 0（不设置过期）。
func optTTL(L *lua.LState, idx int) time.Duration {
	if L.GetTop() < idx {
		return 0
	}
	v := L.Get(idx)
	if v == lua.LNil {
		return 0
	}
	sec := float64(lua.LVAsNumber(v))
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec * float64(time.Second))
}

// optInt64 读取栈位 idx 的可选整数参数，缺省返回 def。
func optInt64(L *lua.LState, idx int, def int64) int64 {
	if L.GetTop() < idx {
		return def
	}
	v := L.Get(idx)
	if v == lua.LNil {
		return def
	}
	return int64(lua.LVAsNumber(v))
}

// ── KV ────────────────────────────────────────────────

// share.set(key, value [, ttlSec]) -> ok, err
func shareSet(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	value := luaToGoValue(L.Get(2))
	ttl := optTTL(L, 3)

	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	err = ctx.Shared.Set(opCtx, key, value, ttl)
	if err != nil {
		return pushShareResult(L, lua.LFalse, err)
	}
	return pushShareResult(L, lua.LTrue, nil)
}

// share.get(key) -> value, ok, err
func shareGet(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled3(L)
	}
	key := L.CheckString(1)

	var val any
	var ok bool
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	val, ok, err = ctx.Shared.Get(opCtx, key)
	if err != nil {
		return pushResult3(L, lua.LNil, false, err)
	}
	if !ok {
		return pushResult3(L, lua.LNil, false, nil)
	}
	return pushResult3(L, goValueToLua(L, val), true, nil)
}

// share.del(key) -> ok, err
func shareDel(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)

	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	err = ctx.Shared.Delete(opCtx, key)
	if err != nil {
		return pushShareResult(L, lua.LFalse, err)
	}
	return pushShareResult(L, lua.LTrue, nil)
}

// share.exists(key) -> exists, err
func shareExists(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)

	var exists bool
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	exists, err = ctx.Shared.Exists(opCtx, key)
	if err != nil {
		return pushShareResult(L, lua.LFalse, err)
	}
	return pushShareResult(L, lua.LBool(exists), nil)
}

// share.expire(key, ttlSec) -> ok, err
func shareExpire(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	ttl := time.Duration(float64(L.CheckNumber(2)) * float64(time.Second))

	var ok bool
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	ok, err = ctx.Shared.Expire(opCtx, key, ttl)
	if err != nil {
		return pushShareResult(L, lua.LFalse, err)
	}
	return pushShareResult(L, lua.LBool(ok), nil)
}

// ── Counter ───────────────────────────────────────────

// share.incr(key [, delta [, ttlSec]]) -> value, err
func shareIncr(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LNil)
	}
	key := L.CheckString(1)
	delta := optInt64(L, 2, 1)
	ttl := optTTL(L, 3)

	var n int64
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	n, err = ctx.Shared.Incr(opCtx, key, delta, ttl)
	if err != nil {
		return pushShareResult(L, lua.LNil, err)
	}
	return pushShareResult(L, lua.LNumber(n), nil)
}

// ── Claim / Lock ──────────────────────────────────────

// share.claim(key, owner [, ttlSec]) -> ok, err
func shareClaim(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	owner := L.CheckString(2)
	ttl := optTTL(L, 3)

	var ok bool
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	ok, err = ctx.Shared.Claim(opCtx, key, owner, ttl)
	if err != nil {
		return pushShareResult(L, lua.LFalse, err)
	}
	return pushShareResult(L, lua.LBool(ok), nil)
}

// share.release(key, owner) -> released, err
func shareRelease(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	owner := L.CheckString(2)

	var ok bool
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	ok, err = ctx.Shared.Release(opCtx, key, owner)
	if err != nil {
		return pushShareResult(L, lua.LFalse, err)
	}
	return pushShareResult(L, lua.LBool(ok), nil)
}

// share.owner(key) -> owner, err
func shareOwner(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LNil)
	}
	key := L.CheckString(1)

	var owner string
	var ok bool
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	owner, ok, err = ctx.Shared.Owner(opCtx, key)
	if err != nil {
		return pushShareResult(L, lua.LNil, err)
	}
	if !ok {
		return pushShareResult(L, lua.LNil, nil)
	}
	return pushShareResult(L, lua.LString(owner), nil)
}

// share.renew(key, owner [, ttlSec]) -> ok, err
func shareRenew(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	owner := L.CheckString(2)
	ttl := optTTL(L, 3)

	var ok bool
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	ok, err = ctx.Shared.Renew(opCtx, key, owner, ttl)
	if err != nil {
		return pushShareResult(L, lua.LFalse, err)
	}
	return pushShareResult(L, lua.LBool(ok), nil)
}

// ── Queue / List ──────────────────────────────────────

// share.queue_push(key, value [, ttlSec]) -> ok, err
func shareQueuePush(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	value := luaToGoValue(L.Get(2))
	ttl := optTTL(L, 3)

	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	err = ctx.Shared.QueuePush(opCtx, key, value, ttl)
	if err != nil {
		return pushShareResult(L, lua.LFalse, err)
	}
	return pushShareResult(L, lua.LTrue, nil)
}

// share.queue_pop(key) -> value, ok, err（ok=false 表示队列为空，用于区分空队列与存储的 null）
func shareQueuePop(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled3(L)
	}
	key := L.CheckString(1)

	var val any
	var ok bool
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	val, ok, err = ctx.Shared.QueuePop(opCtx, key)
	if err != nil {
		return pushResult3(L, lua.LNil, false, err)
	}
	if !ok {
		return pushResult3(L, lua.LNil, false, nil)
	}
	return pushResult3(L, goValueToLua(L, val), true, nil)
}

// share.queue_len(key) -> n, err
func shareQueueLen(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LNil)
	}
	key := L.CheckString(1)

	var n int64
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	n, err = ctx.Shared.QueueLen(opCtx, key)
	if err != nil {
		return pushShareResult(L, lua.LNil, err)
	}
	return pushShareResult(L, lua.LNumber(n), nil)
}

// share.queue_expire(key, ttlSec) -> ok, err
func shareQueueExpire(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	ttl := time.Duration(float64(L.CheckNumber(2)) * float64(time.Second))

	var ok bool
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	ok, err = ctx.Shared.QueueExpire(opCtx, key, ttl)
	if err != nil {
		return pushShareResult(L, lua.LFalse, err)
	}
	return pushShareResult(L, lua.LBool(ok), nil)
}

// ── Hash ──────────────────────────────────────────────

// share.hash_set(key, field, value [, ttlSec]) -> ok, err
func shareHashSet(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	field := L.CheckString(2)
	value := luaToGoValue(L.Get(3))
	ttl := optTTL(L, 4)

	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	err = ctx.Shared.HashSet(opCtx, key, field, value, ttl)
	if err != nil {
		return pushShareResult(L, lua.LFalse, err)
	}
	return pushShareResult(L, lua.LTrue, nil)
}

// share.hash_get(key, field) -> value, ok, err
func shareHashGet(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled3(L)
	}
	key := L.CheckString(1)
	field := L.CheckString(2)

	var val any
	var ok bool
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	val, ok, err = ctx.Shared.HashGet(opCtx, key, field)
	if err != nil {
		return pushResult3(L, lua.LNil, false, err)
	}
	if !ok {
		return pushResult3(L, lua.LNil, false, nil)
	}
	return pushResult3(L, goValueToLua(L, val), true, nil)
}

// share.hash_get_all(key) -> table, err（不存在返回 nil；空 hash 返回 {}）
func shareHashGetAll(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LNil)
	}
	key := L.CheckString(1)

	var m map[string]any
	var ok bool
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	m, ok, err = ctx.Shared.HashGetAll(opCtx, key)
	if err != nil {
		return pushShareResult(L, lua.LNil, err)
	}
	if !ok {
		return pushShareResult(L, lua.LNil, nil)
	}
	tb := L.CreateTable(0, len(m))
	for field, v := range m {
		tb.RawSetString(field, goValueToLua(L, v))
	}
	return pushShareResult(L, tb, nil)
}

// share.hash_del(key, field) -> ok, err
func shareHashDel(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	field := L.CheckString(2)

	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	err = ctx.Shared.HashDelete(opCtx, key, field)
	if err != nil {
		return pushShareResult(L, lua.LFalse, err)
	}
	return pushShareResult(L, lua.LTrue, nil)
}

// share.hash_incr(key, field [, delta [, ttlSec]]) -> value, err
func shareHashIncr(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LNil)
	}
	key := L.CheckString(1)
	field := L.CheckString(2)
	delta := optInt64(L, 3, 1)
	ttl := optTTL(L, 4)

	var n int64
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	n, err = ctx.Shared.HashIncr(opCtx, key, field, delta, ttl)
	if err != nil {
		return pushShareResult(L, lua.LNil, err)
	}
	return pushShareResult(L, lua.LNumber(n), nil)
}

// share.hash_expire(key, ttlSec) -> ok, err
func shareHashExpire(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	ttl := time.Duration(float64(L.CheckNumber(2)) * float64(time.Second))

	var ok bool
	var err error
	opCtx, cancel := opContext(ctx)
	defer cancel()
	ok, err = ctx.Shared.HashExpire(opCtx, key, ttl)
	if err != nil {
		return pushShareResult(L, lua.LFalse, err)
	}
	return pushShareResult(L, lua.LBool(ok), nil)
}
