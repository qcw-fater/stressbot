package script

import (
	"context"
	"time"

	"stressbot/state/shared"

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
// 协作式 I/O（Class B）：每个 Redis 往返经 awaitIO 投递到后台协程执行，调用所在的执行器
// goroutine 在等待窗口内持续 drain 任务队列（跑 listen 回调等），故 Redis 往返不会饿死 Robot
// 的其他协作式工作。操作上下文继承 Robot 生命周期 ctx，并受共享状态 OpTimeout 限制。
//
// 与所有 await_* 一样，share.* 只能在脚本顶层（调度器协程）直接调用，不可置于 pcall/xpcall
// 或 coroutine.create 创建的协程内（否则 fail-loud，见 awaitYield）。
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
	L.Push(lua.LString(shared.ErrNotEnabled.Error()))
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
	return pushResult3(L, lua.LNil, false, shared.ErrNotEnabled)
}

// resultVals / resultVals3 是 pushResult / pushResult3 的协作式渲染版：在执行器 goroutine 上
// 被 awaitIO 的 IORenderer 调用，返回 await 的 Lua 返回值（而非直接 L.Push）。语义完全一致。
func resultVals(first lua.LValue, err error) []lua.LValue {
	if err != nil {
		return []lua.LValue{first, lua.LString(err.Error())}
	}
	return []lua.LValue{first, lua.LNil}
}

func resultVals3(value lua.LValue, ok bool, err error) []lua.LValue {
	third := lua.LNil
	if err != nil {
		third = lua.LString(err.Error())
	}
	return []lua.LValue{value, lua.LBool(ok), third}
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

	return awaitIO(L, "share.set", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		err := ctx.Shared.Set(opCtx, key, value, ttl)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LFalse, err)
			}
			return resultVals(lua.LTrue, nil)
		}
	})
}

// share.get(key) -> value, ok, err
func shareGet(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled3(L)
	}
	key := L.CheckString(1)

	return awaitIO(L, "share.get", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		val, ok, err := ctx.Shared.Get(opCtx, key)
		return func(L *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals3(lua.LNil, false, err)
			}
			if !ok {
				return resultVals3(lua.LNil, false, nil)
			}
			return resultVals3(goValueToLua(L, val), true, nil)
		}
	})
}

// share.del(key) -> ok, err
func shareDel(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)

	return awaitIO(L, "share.del", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		err := ctx.Shared.Delete(opCtx, key)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LFalse, err)
			}
			return resultVals(lua.LTrue, nil)
		}
	})
}

// share.exists(key) -> exists, err
func shareExists(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)

	return awaitIO(L, "share.exists", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		exists, err := ctx.Shared.Exists(opCtx, key)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LFalse, err)
			}
			return resultVals(lua.LBool(exists), nil)
		}
	})
}

// share.expire(key, ttlSec) -> ok, err
func shareExpire(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	ttl := time.Duration(float64(L.CheckNumber(2)) * float64(time.Second))

	return awaitIO(L, "share.expire", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		ok, err := ctx.Shared.Expire(opCtx, key, ttl)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LFalse, err)
			}
			return resultVals(lua.LBool(ok), nil)
		}
	})
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

	return awaitIO(L, "share.incr", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		n, err := ctx.Shared.Incr(opCtx, key, delta, ttl)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LNil, err)
			}
			return resultVals(lua.LNumber(n), nil)
		}
	})
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

	return awaitIO(L, "share.claim", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		ok, err := ctx.Shared.Claim(opCtx, key, owner, ttl)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LFalse, err)
			}
			return resultVals(lua.LBool(ok), nil)
		}
	})
}

// share.release(key, owner) -> released, err
func shareRelease(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	owner := L.CheckString(2)

	return awaitIO(L, "share.release", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		ok, err := ctx.Shared.Release(opCtx, key, owner)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LFalse, err)
			}
			return resultVals(lua.LBool(ok), nil)
		}
	})
}

// share.owner(key) -> owner, err
func shareOwner(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LNil)
	}
	key := L.CheckString(1)

	return awaitIO(L, "share.owner", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		owner, ok, err := ctx.Shared.Owner(opCtx, key)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LNil, err)
			}
			if !ok {
				return resultVals(lua.LNil, nil)
			}
			return resultVals(lua.LString(owner), nil)
		}
	})
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

	return awaitIO(L, "share.renew", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		ok, err := ctx.Shared.Renew(opCtx, key, owner, ttl)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LFalse, err)
			}
			return resultVals(lua.LBool(ok), nil)
		}
	})
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

	return awaitIO(L, "share.queue_push", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		err := ctx.Shared.QueuePush(opCtx, key, value, ttl)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LFalse, err)
			}
			return resultVals(lua.LTrue, nil)
		}
	})
}

// share.queue_pop(key) -> value, ok, err（ok=false 表示队列为空，用于区分空队列与存储的 null）
func shareQueuePop(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled3(L)
	}
	key := L.CheckString(1)

	return awaitIO(L, "share.queue_pop", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		val, ok, err := ctx.Shared.QueuePop(opCtx, key)
		return func(L *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals3(lua.LNil, false, err)
			}
			if !ok {
				return resultVals3(lua.LNil, false, nil)
			}
			return resultVals3(goValueToLua(L, val), true, nil)
		}
	})
}

// share.queue_len(key) -> n, err
func shareQueueLen(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LNil)
	}
	key := L.CheckString(1)

	return awaitIO(L, "share.queue_len", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		n, err := ctx.Shared.QueueLen(opCtx, key)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LNil, err)
			}
			return resultVals(lua.LNumber(n), nil)
		}
	})
}

// share.queue_expire(key, ttlSec) -> ok, err
func shareQueueExpire(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	ttl := time.Duration(float64(L.CheckNumber(2)) * float64(time.Second))

	return awaitIO(L, "share.queue_expire", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		ok, err := ctx.Shared.QueueExpire(opCtx, key, ttl)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LFalse, err)
			}
			return resultVals(lua.LBool(ok), nil)
		}
	})
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

	return awaitIO(L, "share.hash_set", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		err := ctx.Shared.HashSet(opCtx, key, field, value, ttl)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LFalse, err)
			}
			return resultVals(lua.LTrue, nil)
		}
	})
}

// share.hash_get(key, field) -> value, ok, err
func shareHashGet(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled3(L)
	}
	key := L.CheckString(1)
	field := L.CheckString(2)

	return awaitIO(L, "share.hash_get", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		val, ok, err := ctx.Shared.HashGet(opCtx, key, field)
		return func(L *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals3(lua.LNil, false, err)
			}
			if !ok {
				return resultVals3(lua.LNil, false, nil)
			}
			return resultVals3(goValueToLua(L, val), true, nil)
		}
	})
}

// share.hash_get_all(key) -> table, err（不存在返回 nil；空 hash 返回 {}）
func shareHashGetAll(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LNil)
	}
	key := L.CheckString(1)

	return awaitIO(L, "share.hash_get_all", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		m, ok, err := ctx.Shared.HashGetAll(opCtx, key)
		return func(L *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LNil, err)
			}
			if !ok {
				return resultVals(lua.LNil, nil)
			}
			tb := L.CreateTable(0, len(m))
			for field, v := range m {
				tb.RawSetString(field, goValueToLua(L, v))
			}
			return resultVals(tb, nil)
		}
	})
}

// share.hash_del(key, field) -> ok, err
func shareHashDel(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	field := L.CheckString(2)

	return awaitIO(L, "share.hash_del", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		err := ctx.Shared.HashDelete(opCtx, key, field)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LFalse, err)
			}
			return resultVals(lua.LTrue, nil)
		}
	})
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

	return awaitIO(L, "share.hash_incr", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		n, err := ctx.Shared.HashIncr(opCtx, key, field, delta, ttl)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LNil, err)
			}
			return resultVals(lua.LNumber(n), nil)
		}
	})
}

// share.hash_expire(key, ttlSec) -> ok, err
func shareHashExpire(L *lua.LState) int {
	ctx := shareCtx(L)
	if ctx == nil {
		return pushNotEnabled(L, lua.LFalse)
	}
	key := L.CheckString(1)
	ttl := time.Duration(float64(L.CheckNumber(2)) * float64(time.Second))

	return awaitIO(L, "share.hash_expire", func() IORenderer {
		opCtx, cancel := opContext(ctx)
		defer cancel()
		ok, err := ctx.Shared.HashExpire(opCtx, key, ttl)
		return func(_ *lua.LState) []lua.LValue {
			if err != nil {
				return resultVals(lua.LFalse, err)
			}
			return resultVals(lua.LBool(ok), nil)
		}
	})
}
