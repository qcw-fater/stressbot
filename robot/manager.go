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
	}
	for _, r := range robots {
		r.Wait()
		m.stopped.Add(1)
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
