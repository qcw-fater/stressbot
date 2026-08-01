# Trampoline 与脚本入口缓存 CPU 优化设计

## 背景

生产环境在 8 核 16GB Linux 机器上运行 10000 个机器人时，CPU 长期占用约 6.45 核，内存稳定在约 40%。039→040 heap 差分窗口中，监听脚本共触发约 728 万次，当前 trampoline 为每个任务通过两次 `pack` 创建 Lua 表，并用字符串键查找每个脚本入口函数。

现有 Windows 基准 `BenchmarkRunActionScript` 的基线为：

```text
28780 ns/op
2797 B/op
25 allocs/op
```

本次优化以降低 CPU 和 GC 成本为首要目标，允许使用几十 MB 以内的常驻内存换取更直接的热路径。

## 目标

1. 删除 trampoline 稳态交接中的 `pack`、`select`、`unpack` 以及两张临时 Lua 表。
2. 删除每次脚本执行时的 `scriptFnCacheKey` 字符串拼接。
3. 将脚本入口函数缓存命中改为 Lua registry 整数槽直接访问。
4. 删除启动任务时的 `initArgs` 变参切片和 bootstrap/handoff 动态参数切片。
5. 保持现有协程、await、返回值、错误和线程复用语义不变。
6. 保证未来 `execute` 和 `on_message` 都采用单参数时无需修改 trampoline 协议。

## 非目标

- 不修改 `conf/scripts/*.lua` 的业务逻辑或调用方式。
- 不调整协程池容量、线程退役阈值或 Lua 运行时池生命周期。
- 不修改 await 调度、网络收发、监控指标或错误码体系。
- 不通过 `GOGC`、`GOMEMLIMIT` 等运行时参数掩盖分配问题。

## 方案选择

采用“每个脚本入口独立 registry 整数槽”的 CPU 优先方案。

未采用的两个备选方案：

- 在一个固定整数槽中保存 Go map：常驻内存较低，但热路径仍需对 `(scriptName, entry)` 做 Go 哈希查找。
- 预生成复合字符串键：只能消除字符串拼接，仍保留 Lua 字符串哈希查找。

直接整数槽能让命中路径最终收敛为 `RawGetInt(slot)`，符合本次用少量内存换 CPU 的目标。

## Registry 槽布局

整数槽按以下顺序分配：

```text
1  Context userdata
2  trampoline 主函数
3  trampoline DONE 哨兵
4+ 各脚本入口函数
```

`globalBaselineKey` 仍使用字符串键，因为它只在脚本首次加载和 LState 归还时使用，不属于每任务热路径。

每个预编译脚本预留两个连续入口槽：

```text
execute
on_message
```

当前 `conf/scripts` 有 74 个脚本，最多新增 148 个入口槽。按 `lua.LValue` 接口值的有效元素估算，10000 个 LState 约增加 24MB；考虑 Go 切片容量增长，实际可能达到几十 MB。该成本在本次 CPU 优先约束内可接受。

## 预编译脚本结构

`RuntimePool.precompiled` 的值从单独的 `*lua.FunctionProto` 调整为包含以下信息的结构：

```go
type precompiledScript struct {
    proto          *lua.FunctionProto
    executeSlot    int
    onMessageSlot  int
}
```

入口类型使用内部枚举，不在热路径比较 `"execute"` 和 `"on_message"` 字符串：

```go
type scriptEntry uint8

const (
    scriptEntryExecute scriptEntry = iota + 1
    scriptEntryOnMessage
)
```

预编译注册统一经过一个内部方法。首次注册脚本时分配两个连续槽；同名脚本被重新注册时复用已有槽，只替换 proto，避免槽号无界增长。生产预编译、测试辅助函数和 benchmark 均使用该注册方法，禁止直接写 `precompiled` map。

`loadScriptFn` 的缓存命中路径为：

1. 通过 `precompiled[scriptName]` 获得预编译结构。
2. 根据 `scriptEntry` 直接选择整数槽。
3. `reg.RawGetInt(slot)` 返回缓存函数。

缓存未命中时才实例化 chunk、读取对应全局入口、移除入口全局、写入 `RawSetInt(slot, target)` 并更新 global baseline。错误行为与现状一致。

## 无表 trampoline 协议

Lua trampoline 改为固定参数协议：

```lua
return function(sentinel, fn, argc, a1, a2)
    local yield = coroutine.yield
    while true do
        if argc == 1 then
            fn, argc, a1, a2 = yield(sentinel, fn(a1))
        elseif argc == 2 then
            fn, argc, a1, a2 = yield(sentinel, fn(a1, a2))
        else
            error("蹦床不支持参数数量: " .. tostring(argc))
        end
    end
end
```

Lua 的函数调用位于 `yield` 的最后一个实参位置，因此会自然展开全部返回值并保留 nil 洞，不再需要 `n = select('#', ...)` 记录返回值数量。

业务函数内部发生 await 时，参数求值尚未结束，WaitSpec 仍直接穿过 trampoline 交给 Go 调度器；恢复后业务函数从原位置继续，最终返回值再交给 DONE yield。trampoline 不增加或减少真实等待、系统调用或网络操作。

## Go 侧任务参数

`coroutine` 删除 `initArgs []lua.LValue`，改为固定字段：

```go
argc uint8
arg1 lua.LValue
arg2 lua.LValue
```

`startCoroutine` 接收 `scriptEntry`、显式 `argc` 和两个固定参数。当前调用约定：

- action/boolean：`argc=1`，`arg1=robot userdata`，`arg2=LNil`。
- listen：`argc=2`，`arg1=robot userdata`，`arg2=message`。

bootstrap 通过固定实参直接调用 `Resume(thread, trampFn, sentinel, fn, argc, arg1, arg2)`；handoff 通过固定实参直接调用 `Resume(thread, nil, fn, argc, arg1, arg2)`，不再执行动态 `make` 和 append。

任务关闭时清空 `fn`、`arg1`、`arg2`，避免 Go 侧 coroutine 临时延长脚本函数或消息值生命周期。Lua trampoline 在 DONE yield 期间仍会持有上一任务参数，生命周期不超过该 trampoline thread 的下一次 handoff 或退役；这不扩大现有 idle-thread 数量和退役上限。

未来若 `on_message` 改为单参数，只需调用方传 `argc=1` 和 `arg2=LNil`，trampoline 无需修改。

## 错误与兼容性

- 仅允许 `argc=1` 或 `argc=2`；其他值在 Lua 侧 fail-loud，Go 侧按 `ResumeError` 处理并弃用该 thread。
- 业务脚本 error 继续穿透 trampoline，由现有 `ResumeError` 分支包装。
- DONE 哨兵判定、WaitSpec 判定、非法 yield 判定顺序不变。
- await 被 `pcall/xpcall` 或用户 coroutine 吞掉时，继续由现有 panic 兜底与父栈残留检查报错。
- action 仍以第一个返回值判断 nil/err table；boolean 仍校验第一个返回值为 bool；listen 仍忽略返回值。
- 脚本首次加载、全局清理和 LState 跨 Robot 复用语义不变。

## 测试策略

实施采用 TDD，先添加会在旧实现上失败的分配约束，再修改生产代码。

### 分配测试

- 缓存预热后的 `loadScriptFn` 命中必须达到 `0 allocs/op`。
- `BenchmarkRunActionScript` 保留现有基准并记录优化前后结果。
- 新增稳态 allocation regression，目标不高于 `10 allocs/op`；当前基线为 `25 allocs/op`。

### 协议正确性

- 同一 trampoline thread 按 `execute(1参) → on_message(2参) → execute(1参)` 复用，参数不串位。
- 直接构造单参数 `on_message` 任务，验证未来入口签名兼容。
- 验证零返回值、单个 nil、false、err table、多返回值和中间 nil 洞。
- 验证一次及多次 await 后原位恢复。
- 验证主 action await 期间嵌套 listen 使用独立 thread。
- 验证脚本 error、非法 yield、pcall 吞 await 后 thread 被弃用，后续任务不受污染。
- 验证满 512 个任务后 thread 正常退役。

### 函数缓存正确性

- 同名入口在不同脚本之间不串缓存。
- 同一脚本的 `execute` 与 `on_message` 不串缓存。
- 同名脚本重新注册 proto 时复用原槽，不增加槽计数。
- LState 归还再获取后仍复用该 LState 已加载的入口函数，与当前行为一致。

## 验收

代码完成后执行：

1. 聚焦 `script` 包单元测试和 benchmark。
2. `go test ./...`。
3. `go build ./...`。
4. `cmd/web` 下执行 `npx tsc -b` 和 `npm run test`。
5. 使用当前 flow 做配置校验。
6. 本地 Agent 运行 2～5 分钟并审查 error/warn 日志。
7. 下一轮 Linux 10000 人压测对比 CPU、alloc_space、GC CPU，并确认协程错误日志没有新增。

本地验证只能证明功能与分配回归；最终 CPU 收益以同版本、同流量、同阶段的 Linux A/B pprof 为准。
