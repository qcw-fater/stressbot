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

// RampUpConfig 渐进式加压配置。
type RampUpConfig struct {
	Stages []RampUpStage
}

// RampUpStage 单个加压阶段。
type RampUpStage struct {
	Count       int // 本阶段新增 bot 数
	Concurrency int // 覆盖全局并发数，0 则用全局值
	HoldSec     int // 阶段间等待秒数
}

// ManagerConfig 机器人管理器配置
type ManagerConfig struct {
	AccountPrefix  string
	StartNumber    int
	Count          int
	ConcurrentNum  int
	StateExtra     map[string]string
	Adapter        adapter.Adapter
	RequestTimeout time.Duration
	MainService    string
	HTTPTimeout    time.Duration
	RampUp         *RampUpConfig
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

// startBatch 创建 [fromIndex, fromIndex+count) 的 bot，使用 conc 做批量限速。
func (m *Manager) startBatch(fromIndex, count, conc int) error {
	for i := 0; i < count; i++ {
		if m.ctx.Err() != nil {
			return m.ctx.Err()
		}

		id := m.cfg.StartNumber + fromIndex + i
		account := fmt.Sprintf("%s%d", m.cfg.AccountPrefix, id)

		r := NewRobot(Config{
			ID:         id,
			Account:    account,
			StateExtra: m.cfg.StateExtra,
		}, m.flow, m.factory, m.cfg.Adapter, m.dialer, m.luaPool,
			m.cfg.RequestTimeout, m.cfg.MainService)

		m.mu.Lock()
		m.robots = append(m.robots, r)
		m.mu.Unlock()

		r.Start()
		m.started.Add(1)

		if conc > 0 && (i+1)%conc == 0 {
			stresslog.Info("[MANAGER] 批量进度",
				zap.Int("batchCreated", i+1),
				zap.Int("batchTotal", count))
			select {
			case <-m.ctx.Done():
				return m.ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	return nil
}

// StartAll 一次性创建全部机器人。
func (m *Manager) StartAll() error {
	stresslog.Info("[MANAGER] 开始创建机器人",
		zap.Int("count", m.cfg.Count),
		zap.Int("concurrent", m.cfg.ConcurrentNum))

	if err := m.startBatch(0, m.cfg.Count, m.cfg.ConcurrentNum); err != nil {
		return err
	}

	stresslog.Info("[MANAGER] 全部机器人已启动", zap.Int("count", m.cfg.Count))
	return nil
}

// StartWithRampUp 分阶段创建机器人。
func (m *Manager) StartWithRampUp() error {
	stages := m.cfg.RampUp.Stages
	total := 0
	for _, s := range stages {
		total += s.Count
	}

	stresslog.Info("[MANAGER] 开始渐进式加压",
		zap.Int("stages", len(stages)),
		zap.Int("totalBots", total),
		zap.Int("defaultConcurrent", m.cfg.ConcurrentNum))

	offset := 0
	for i, stage := range stages {
		conc := m.cfg.ConcurrentNum
		if stage.Concurrency > 0 {
			conc = stage.Concurrency
		}

		stresslog.Info("[MANAGER] 启动阶段",
			zap.Int("stage", i+1),
			zap.Int("count", stage.Count),
			zap.Int("concurrency", conc),
			zap.Int("holdSec", stage.HoldSec))

		if err := m.startBatch(offset, stage.Count, conc); err != nil {
			return err
		}
		offset += stage.Count

		stresslog.Info("[MANAGER] 阶段完成",
			zap.Int("stage", i+1),
			zap.Int("running", offset))

		// 阶段间等待（最后一个阶段不等）
		if i < len(stages)-1 && stage.HoldSec > 0 {
			stresslog.Info("[MANAGER] 阶段保持",
				zap.Int("stage", i+1),
				zap.Int("holdSec", stage.HoldSec),
				zap.Int("running", offset))
			select {
			case <-m.ctx.Done():
				return m.ctx.Err()
			case <-time.After(time.Duration(stage.HoldSec) * time.Second):
			}
		}
	}

	stresslog.Info("[MANAGER] 渐进式加压完成，全部机器人已启动", zap.Int("count", offset))
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
	stresslog.Info("[MANAGER] 全部机器人已停止")
}
