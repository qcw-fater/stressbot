package robot

import (
	"context"
	"fmt"
	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/script"
	stresslog "stressbot/utils/log"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// ManagerConfig 机器人管理器配置
type ManagerConfig struct {
	AccountPrefix  string
	StartNumber    int
	Count          int
	ConcurrentNum  int
	AuthBaseURL    string
	AuthExtra      map[string]string
	Adapter        adapter.Adapter
	RequestTimeout time.Duration
	MainService    string
	HTTPTimeout    time.Duration
}

// Manager 机器人管理器。
type Manager struct {
	cfg     ManagerConfig
	flow    *engine.TaskFlow
	factory *protox.Factory
	dialer  *network.Dialer
	luaPool *script.RuntimePool
	robots  []*Robot
	mu      sync.RWMutex
	ctx     context.Context
	cancel  context.CancelFunc
	started atomic.Int32
	stopped atomic.Int32
}

// NewManager 创建机器人管理器
func NewManager(cfg ManagerConfig, flow *engine.TaskFlow, factory *protox.Factory,
	dialer *network.Dialer, luaPool *script.RuntimePool) *Manager {

	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:     cfg,
		flow:    flow,
		factory: factory,
		dialer:  dialer,
		luaPool: luaPool,
		robots:  make([]*Robot, 0, cfg.Count),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// StartAll 批量启动机器人
func (m *Manager) StartAll() error {
	stresslog.Info("[MANAGER] 开始创建机器人", zap.Int("count", m.cfg.Count), zap.Int("concurrent", m.cfg.ConcurrentNum))

	for i := 0; i < m.cfg.Count; i++ {
		if m.ctx.Err() != nil {
			return m.ctx.Err()
		}

		id := m.cfg.StartNumber + i
		account := fmt.Sprintf("%s%d", m.cfg.AccountPrefix, id)

		r := NewRobot(Config{
			ID:          id,
			Account:     account,
			AuthBaseURL: m.cfg.AuthBaseURL,
			AuthExtra:   m.cfg.AuthExtra,
		}, m.flow, m.factory, m.cfg.Adapter, m.dialer, m.luaPool,
			m.cfg.RequestTimeout, m.cfg.MainService)

		m.mu.Lock()
		m.robots = append(m.robots, r)
		m.mu.Unlock()

		r.Start()
		m.started.Add(1)

		if m.cfg.ConcurrentNum > 0 && (i+1)%m.cfg.ConcurrentNum == 0 {
			stresslog.Info("[MANAGER] 机器人启动进度", zap.Int("started", i+1), zap.Int("total", m.cfg.Count))
			select {
			case <-m.ctx.Done():
				return m.ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}

	stresslog.Info("[MANAGER] 全部机器人已启动", zap.Int("count", m.cfg.Count))
	return nil
}

// StopAll 停止所有机器人并等待执行 goroutine 结束。
func (m *Manager) StopAll() {
	m.cancel()
	m.mu.RLock()
	robots := make([]*Robot, len(m.robots))
	copy(robots, m.robots)
	m.mu.RUnlock()

	for _, r := range robots {
		r.Close()
		m.stopped.Add(1)
	}
	// 等待所有 Robot 执行 goroutine 退出，避免进程退出时仍在写日志或发包
	for _, r := range robots {
		r.Wait()
	}
	stresslog.Info("[MANAGER] 全部机器人已停止")
}

// GetStats 获取运行统计
func (m *Manager) GetStats() (started, stopped int) {
	return int(m.started.Load()), int(m.stopped.Load())
}

// GetRobot 获取指定索引的机器人
func (m *Manager) GetRobot(index int) *Robot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if index < 0 || index >= len(m.robots) {
		return nil
	}
	return m.robots[index]
}

// Count 返回已创建的机器人数量
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.robots)
}

// RunConfig 任务运行参数（Agent 模式下由 Admin 下发）。
type RunConfig struct {
	StartNumber int
	TotalBots   int
	Concurrency int
}

// RunWithContext 启动机器人并阻塞，直到 ctx 取消或所有机器人退出。
// 用于 Agent 模式：外部通过 cancel ctx 触发停止。
func (m *Manager) RunWithContext(ctx context.Context, cfg RunConfig) error {
	// 覆盖配置
	if cfg.StartNumber > 0 {
		m.cfg.StartNumber = cfg.StartNumber
	}
	if cfg.TotalBots > 0 {
		m.cfg.Count = cfg.TotalBots
	}
	if cfg.Concurrency > 0 {
		m.cfg.ConcurrentNum = cfg.Concurrency
	}

	if err := m.StartAll(); err != nil {
		return err
	}

	// 等待 ctx 取消或所有机器人退出
	done := make(chan struct{})
	go func() {
		for {
			m.mu.RLock()
			n := len(m.robots)
			m.mu.RUnlock()
			_, st := m.GetStats()
			if int(st) >= n && n > 0 {
				close(done)
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()

	select {
	case <-ctx.Done():
		m.StopAll()
		return ctx.Err()
	case <-done:
		return nil
	}
}
