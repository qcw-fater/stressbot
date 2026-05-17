// Package utils 提供通用工具函数
// work_pool.go 实现了基于 ants 的协程池管理，支持优雅关闭和 goroutine 追踪
package utils

import (
	"errors"
	"fmt"
	"runtime"
	"stressbot/utils/log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panjf2000/ants/v2"
	"go.uber.org/zap"
)

// PoolConfig 协程池配置
type PoolConfig struct {
	Cap              int           `mapstructure:"cap"`                // 协程池容量，0 表示无限制
	ExpiryDuration   time.Duration `mapstructure:"expiry_duration"`    // 空闲 goroutine 存活时间
	PreAlloc         bool          `mapstructure:"pre_alloc"`          // 是否预分配 goroutine
	MaxBlockingTasks int           `mapstructure:"max_blocking_tasks"` // 最大阻塞任务数
	Nonblocking      bool          `mapstructure:"nonblocking"`        // 是否非阻塞模式
	ShutdownTimeout  time.Duration `mapstructure:"shutdown_timeout"`   // 优雅关闭超时时间
}

// goroutineInfo 存储 goroutine 追踪信息
type goroutineInfo struct {
	id      uint32
	started time.Time
	caller  string
}

// WorkPool 协程池
type WorkPool struct {
	pool       *ants.Pool
	goroutines sync.Map // uint32 -> goroutineInfo，用于追踪 goroutine

	stopped   atomic.Bool
	stopCh    chan struct{}
	waiting   atomic.Int64 // 等待执行的任务数
	submitted atomic.Int64 // 已提交的任务数
	completed atomic.Int64 // 已完成的任务数
	failed    atomic.Int64 // 失败的任务数
	goId      uint32       // goroutine ID 计数器
	goCount   atomic.Int32 // 当前运行中的 goroutine 数

	cfg *PoolConfig
}

// 错误定义
var (
	ErrPoolStopped  = errors.New("work pool is stopped")
	ErrSubmitFailed = errors.New("submit task to pool failed")
)

var (
	workPool     *WorkPool
	workPoolOnce sync.Once
)

// InitWorkPool 初始化协程池（单例模式）
func InitWorkPool(cfg *PoolConfig) {
	workPoolOnce.Do(func() {
		if cfg == nil {
			cfg = &PoolConfig{
				Cap:             0,
				ExpiryDuration:  60 * time.Second,
				ShutdownTimeout: 5 * time.Second,
			}
		}

		opts := ants.Options{
			ExpiryDuration:   cfg.ExpiryDuration,
			PreAlloc:         cfg.PreAlloc,
			MaxBlockingTasks: cfg.MaxBlockingTasks,
			Nonblocking:      cfg.Nonblocking,
			PanicHandler: func(err interface{}) {
				log.DPanic("goroutine pool panic", zap.Any("error", err))
			},
		}

		pool, err := ants.NewPool(cfg.Cap, ants.WithOptions(opts))
		if err != nil {
			panic(err)
		}

		workPool = &WorkPool{
			pool:   pool,
			stopCh: make(chan struct{}),
			cfg:    cfg,
		}
		log.Info("work pool initialized",
			zap.Int("cap", pool.Cap()),
			zap.Duration("expiry_duration", cfg.ExpiryDuration))
	})
}

// GetWorkPool 获取协程池实例
func GetWorkPool() *WorkPool {
	if workPool == nil {
		InitWorkPool(nil)
	}
	return workPool
}

// IsStopped 检查协程池是否已停止
func (p *WorkPool) IsStopped() bool {
	return p.stopped.Load()
}

// StopChan 获取停止通道，用于监听关闭信号
func (p *WorkPool) StopChan() <-chan struct{} {
	return p.stopCh
}

// submit 提交带停止通知的任务
func (p *WorkPool) submit(task func(stopCh <-chan struct{})) error {
	if p.IsStopped() {
		return ErrPoolStopped
	}

	p.waiting.Add(1)
	p.submitted.Add(1)

	id := atomic.AddUint32(&p.goId, 1)
	count := p.goCount.Add(1)
	caller := p.getCaller()

	p.goroutines.Store(id, &goroutineInfo{
		id:      id,
		started: time.Now(),
		caller:  caller,
	})

	log.Debug("goroutine start",
		zap.Uint32("id", id),
		zap.Int32("count", count),
		zap.String("caller", caller))

	err := p.pool.Submit(func() {
		defer func() {
			if err := recover(); err != nil {
				p.failed.Add(1)
				log.DPanic("goroutine panic",
					zap.Uint32("id", id),
					zap.Any("error", err))
			}
			p.waiting.Add(-1)
			p.completed.Add(1)
			p.goCount.Add(-1)
			p.goroutines.Delete(id)
			log.Debug("goroutine end",
				zap.Uint32("id", id),
				zap.Int32("count", count),
				zap.String("caller", caller))
		}()

		task(p.stopCh)
	})

	if err != nil {
		p.waiting.Add(-1)
		log.Error("submit task failed", zap.Error(err))
		return ErrSubmitFailed
	}
	return nil
}

// Submit 提交普通任务
func (p *WorkPool) Submit(task func()) error {
	return p.submit(func(_ <-chan struct{}) { task() })
}

// SubmitWithStop 提交带停止通知的任务
func (p *WorkPool) SubmitWithStop(task func(stopCh <-chan struct{})) error {
	return p.submit(task)
}

// Go 提交普通任务并忽略错误
func (p *WorkPool) Go(task func()) {
	_ = p.submit(func(_ <-chan struct{}) { task() })
}

// GoWithStop 提交带停止通知的任务并忽略错误
func (p *WorkPool) GoWithStop(task func(stopCh <-chan struct{})) {
	_ = p.submit(task)
}

// Shutdown 优雅关闭协程池，等待所有任务完成或超时
func (p *WorkPool) Shutdown() {
	if !p.stopped.CompareAndSwap(false, true) {
		return
	}

	timeout := p.cfg.ShutdownTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	log.Info("work pool shutting down...",
		zap.Int("running", p.Running()),
		zap.Int64("waiting", p.waiting.Load()),
		zap.Duration("timeout", timeout))

	close(p.stopCh)

	deadline := time.After(timeout)
	timer := time.NewTicker(time.Millisecond)
	defer timer.Stop()

	for {
		select {
		case <-deadline:
			log.Error("shutdown timeout",
				zap.Int64("remaining", p.waiting.Load()))
			p.printLeakedGoroutines()
			return
		case <-timer.C:
			if p.waiting.Load() == 0 {
				p.pool.Release()
				log.Info("work pool shutdown complete")
				return
			}
		}
	}
}

// printLeakedGoroutines 打印泄漏的 goroutine 信息
func (p *WorkPool) printLeakedGoroutines() {
	p.goroutines.Range(func(key, value interface{}) bool {
		info := value.(*goroutineInfo)
		log.Error("leaked goroutine",
			zap.Uint32("id", info.id),
			zap.String("caller", info.caller),
			zap.Duration("running", time.Since(info.started)))
		return true
	})
}

// getCaller 获取调用者信息
func (p *WorkPool) getCaller() string {
	_, file, line, _ := runtime.Caller(3)
	i := strings.LastIndex(file, "/") + 1
	i = strings.LastIndex((string)(([]byte(file))[:i-1]), "/") + 1

	return fmt.Sprintf("%s:%d", (string)(([]byte(file))[i:]), line)
}

// === 状态查询方法 ===

// Running 获取正在运行的 goroutine 数
func (p *WorkPool) Running() int { return p.pool.Running() }

// Free 获取空闲 goroutine 数
func (p *WorkPool) Free() int { return p.pool.Free() }

// Cap 获取协程池容量
func (p *WorkPool) Cap() int { return p.pool.Cap() }

// Waiting 获取等待中的任务数（ants 池级别）
func (p *WorkPool) Waiting() int { return p.pool.Waiting() }

// GoCount 获取当前管理的 goroutine 数
func (p *WorkPool) GoCount() int32 { return p.goCount.Load() }

// Stats 获取任务统计信息
func (p *WorkPool) Stats() (submitted, completed, failed int64) {
	return p.submitted.Load(), p.completed.Load(), p.failed.Load()
}

// Tune 动态调整协程池容量
func (p *WorkPool) Tune(size int) { p.pool.Tune(size) }

// Reboot 重启协程池
func (p *WorkPool) Reboot() { p.pool.Reboot() }
