package robot

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"stressbot/engine"
	stresslog "stressbot/log"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/script"
)

// ManagerConfig 机器人管理器配置
type ManagerConfig struct {
	AccountPrefix string // 账号前缀
	StartNumber   int    // 起始编号
	Count         int    // 机器人数量
	ConcurrentNum int    // 每秒启动数量（限速）
	AuthBaseURL   string // Auth 服务基础 URL
	Version       string // 客户端版本号
	Channel       string // 渠道标识
	Platform      string // 平台标识
}

// Manager 机器人管理器。
// 负责批量创建、限速启动、监控和销毁 Robot 实例。
type Manager struct {
	cfg      ManagerConfig
	flow     *engine.TaskFlow
	factory  *protox.Factory
	protocol *network.Protocol
	dialer   *network.Dialer
	luaPool  *script.RuntimePool
	robots   []*Robot
	mu       sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc
	started  atomic.Int32 // 已启动数量
	stopped  atomic.Int32 // 已停止数量
}

// NewManager 创建机器人管理器
func NewManager(cfg ManagerConfig, flow *engine.TaskFlow, factory *protox.Factory,
	protocol *network.Protocol, dialer *network.Dialer, luaPool *script.RuntimePool) *Manager {

	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:      cfg,
		flow:     flow,
		factory:  factory,
		protocol: protocol,
		dialer:   dialer,
		luaPool:  luaPool,
		robots:   make([]*Robot, 0, cfg.Count),
		ctx:      ctx,
		cancel:   cancel,
	}
}

// StartAll 批量启动机器人，按 ConcurrentNum 限速
func (m *Manager) StartAll() error {
	stresslog.InfoF("[MANAGER] 开始创建 %d 个机器人，每秒启动 %d 个", m.cfg.Count, m.cfg.ConcurrentNum)

	for i := 0; i < m.cfg.Count; i++ {
		if m.ctx.Err() != nil {
			return m.ctx.Err()
		}

		id := m.cfg.StartNumber + i
		account := fmt.Sprintf("%s%d", m.cfg.AccountPrefix, id)

		r := NewRobot(RobotConfig{
			ID:          id,
			Account:     account,
			AuthBaseURL: m.cfg.AuthBaseURL,
			Version:     m.cfg.Version,
			Channel:     m.cfg.Channel,
			Platform:    m.cfg.Platform,
		}, m.flow, m.factory, m.protocol, m.dialer, m.luaPool)

		m.mu.Lock()
		m.robots = append(m.robots, r)
		m.mu.Unlock()

		r.Start()
		m.started.Add(1)

		// 限速
		if m.cfg.ConcurrentNum > 0 && (i+1)%m.cfg.ConcurrentNum == 0 {
			stresslog.InfoF("[MANAGER] 已启动 %d/%d 个机器人", i+1, m.cfg.Count)
			select {
			case <-m.ctx.Done():
				return m.ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}

	stresslog.InfoF("[MANAGER] 全部 %d 个机器人已启动", m.cfg.Count)
	return nil
}

// StopAll 停止所有机器人
func (m *Manager) StopAll() {
	m.cancel()
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, r := range m.robots {
		r.Close()
		m.stopped.Add(1)
	}
	stresslog.InfoF("[MANAGER] 全部机器人已停止")
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
