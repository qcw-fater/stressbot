// Package workpool 提供基于 ants 的协程池管理，支持优雅关闭和 goroutine 追踪。
package workpool

import (
	"errors"
	"fmt"
	"runtime"
	"stressbot/internal/stresslog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panjf2000/ants/v2"
	"go.uber.org/zap"
)

// 协程池硬编码默认参数。
// 原 PoolConfig 配置项已被移除（mapstructure tag 无法被 TOML 解析、唯一调用点永远传 nil，
// 形同虚设）。如需调优，修改以下常量。
const (
	poolCap             = 0                // 0 = 无限制
	poolExpiryDuration  = 60 * time.Second // 空闲 goroutine 存活时间
	poolShutdownTimeout = 5 * time.Second  // 优雅关闭超时
)

// goroutineInfo 存储 goroutine 追踪信息
type goroutineInfo struct {
	id      uint32
	started time.Time
	caller  string
}

// Pool 管理受控 goroutine 的提交、追踪与关闭。
type Pool struct {
	pool       *ants.Pool
	goroutines sync.Map // uint32 -> goroutineInfo，用于追踪 goroutine
	wg         sync.WaitGroup

	stopped   atomic.Bool
	stopCh    chan struct{}
	waiting   atomic.Int64  // 等待执行的任务数
	submitted atomic.Int64  // 已提交的任务数
	completed atomic.Int64  // 已完成的任务数
	failed    atomic.Int64  // 失败的任务数
	goID      atomic.Uint32 // goroutine ID 计数器
	goCount   atomic.Int32  // 当前运行中的 goroutine 数
}

// 错误定义
var (
	ErrPoolStopped  = errors.New("工作协程池已停止")
	ErrSubmitFailed = errors.New("提交任务到协程池失败")
)

var (
	workPool     *Pool
	workPoolOnce sync.Once
)

// Init 初始化默认协程池（单例模式）。
// 参数已移除：原 PoolConfig 永远传 nil，现使用包级常量 poolCap/poolExpiryDuration/poolShutdownTimeout。
func Init() {
	workPoolOnce.Do(func() {
		opts := ants.Options{
			ExpiryDuration: poolExpiryDuration,
			PanicHandler: func(err any) {
				stresslog.DPanic("goroutine pool panic", zap.Any("error", err))
			},
		}

		pool, err := ants.NewPool(poolCap, ants.WithOptions(opts))
		if err != nil {
			panic(err)
		}

		workPool = &Pool{
			pool:   pool,
			stopCh: make(chan struct{}),
		}
		stresslog.Info("work pool initialized",
			zap.Int("cap", pool.Cap()),
			zap.Duration("expiry_duration", poolExpiryDuration))
	})
}

// Default 获取默认协程池实例。
func Default() *Pool {
	Init()
	return workPool
}

// IsStopped 检查协程池是否已停止
func (p *Pool) IsStopped() bool {
	return p.stopped.Load()
}

// StopChan 获取停止通道，用于监听关闭信号
func (p *Pool) StopChan() <-chan struct{} {
	return p.stopCh
}

// submit 提交带停止通知的任务。
//
// 性能：getCaller / goroutines.Store / start-end 双向 Debug 日志只在 Debug 级别开启时执行。
// info 级别下大规模启动（万级 robot 创建数十万 goroutine）单次 submit 开销从 1-10us 降到 <100ns。
// 副作用：info 级别下 goroutines map 始终为空，printLeakedGoroutines 仅在 debug 启动时有效。
func (p *Pool) submit(task func(stopCh <-chan struct{})) error {
	if p.IsStopped() {
		return ErrPoolStopped
	}

	// 先登记 wg，再二次检查 stopped：Shutdown 会「置位 stopped → wg.Wait()」。
	// 若在首次检查与此处之间 Shutdown 置位，二次检查命中后 Done 抵消计数并退出，
	// 保证已 Add 的计数不会遗留，避免 Shutdown 的 wg.Wait() 永久阻塞。
	p.wg.Add(1)
	if p.IsStopped() {
		p.wg.Done()
		return ErrPoolStopped
	}

	p.waiting.Add(1)
	p.submitted.Add(1)
	p.goCount.Add(1)

	debugOn := stresslog.DebugEnabled()
	var (
		id     uint32
		count  int32
		caller string
	)
	if debugOn {
		id = p.goID.Add(1)
		count = p.goCount.Load()
		caller = p.getCaller()
		p.goroutines.Store(id, &goroutineInfo{
			id:      id,
			started: time.Now(),
			caller:  caller,
		})
		stresslog.Debug("goroutine start",
			zap.Uint32("id", id),
			zap.Int32("count", count),
			zap.String("caller", caller))
	}

	err := p.pool.Submit(func() {
		defer func() {
			p.wg.Done()
			if err := recover(); err != nil {
				p.failed.Add(1)
				stresslog.DPanic("goroutine panic",
					zap.Uint32("id", id),
					zap.Any("error", err))
			}
			p.waiting.Add(-1)
			p.completed.Add(1)
			p.goCount.Add(-1)
			if debugOn {
				p.goroutines.Delete(id)
				stresslog.Debug("goroutine end",
					zap.Uint32("id", id),
					zap.Int32("count", count),
					zap.String("caller", caller))
			}
		}()

		task(p.stopCh)
	})

	if err != nil {
		// 补齐 wg.Done：提交失败时任务体不会执行，其 defer 中的 wg.Done 也不会触发，
		// 若不在此抵消，Shutdown 的 wg.Wait() 将永久阻塞至超时。
		p.wg.Done()
		p.waiting.Add(-1)
		p.goCount.Add(-1)
		if debugOn {
			p.goroutines.Delete(id)
		}
		stresslog.Error("submit task failed", zap.Error(err))
		return ErrSubmitFailed
	}
	return nil
}

// Submit 提交普通任务
func (p *Pool) Submit(task func()) error {
	return p.submit(func(_ <-chan struct{}) { task() })
}

// SubmitWithStop 提交带停止通知的任务
func (p *Pool) SubmitWithStop(task func(stopCh <-chan struct{})) error {
	return p.submit(task)
}

// Go 提交普通任务并忽略错误
func (p *Pool) Go(task func()) {
	_ = p.submit(func(_ <-chan struct{}) { task() })
}

// GoWithStop 提交带停止通知的任务并忽略错误
func (p *Pool) GoWithStop(task func(stopCh <-chan struct{})) {
	_ = p.submit(task)
}

// Shutdown 优雅关闭协程池，等待所有任务完成或超时
func (p *Pool) Shutdown() {
	if !p.stopped.CompareAndSwap(false, true) {
		return
	}

	timeout := poolShutdownTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	stresslog.Info("work pool shutting down...",
		zap.Int("running", p.Running()),
		zap.Int64("waiting", p.waiting.Load()),
		zap.Duration("timeout", timeout))

	close(p.stopCh)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.pool.Release()
		stresslog.Info("work pool shutdown complete")
	case <-time.After(timeout):
		stresslog.Error("shutdown timeout",
			zap.Int64("remaining", p.waiting.Load()))
		p.printLeakedGoroutines()
	}
}

// printLeakedGoroutines 打印泄漏的 goroutine 信息
func (p *Pool) printLeakedGoroutines() {
	p.goroutines.Range(func(_, value any) bool {
		info := value.(*goroutineInfo)
		stresslog.Error("leaked goroutine",
			zap.Uint32("id", info.id),
			zap.String("caller", info.caller),
			zap.Duration("running", time.Since(info.started)))
		return true
	})
}

// getCaller 获取调用者信息
func (p *Pool) getCaller() string {
	pcs := make([]uintptr, 1)
	n := runtime.Callers(4, pcs)
	if n == 0 {
		return "?:0"
	}
	frames := runtime.CallersFrames(pcs)
	frame, _ := frames.Next()
	return fmt.Sprintf("%s:%d", frame.Function, frame.Line)
}

// === 状态查询方法 ===

// Running 获取正在运行的 goroutine 数
func (p *Pool) Running() int { return p.pool.Running() }

// Free 获取空闲 goroutine 数
func (p *Pool) Free() int { return p.pool.Free() }

// Cap 获取协程池容量
func (p *Pool) Cap() int { return p.pool.Cap() }

// Waiting 获取等待中的任务数（ants 池级别）
func (p *Pool) Waiting() int { return p.pool.Waiting() }

// GoCount 获取当前管理的 goroutine 数
func (p *Pool) GoCount() int32 { return p.goCount.Load() }

// Stats 获取任务统计信息
func (p *Pool) Stats() (submitted, completed, failed int64) {
	return p.submitted.Load(), p.completed.Load(), p.failed.Load()
}

// Tune 动态调整协程池容量
func (p *Pool) Tune(size int) { p.pool.Tune(size) }

// Reboot 重启协程池
func (p *Pool) Reboot() { p.pool.Reboot() }
