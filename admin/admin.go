package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// AdminServer Admin HTTP 服务器。
type AdminServer struct {
	cfg Config

	tasks      *TaskStore
	agents     *AgentRegistry
	binaries   *BinaryStore
	aggregator *MetricsAggregator
	upgrader   *UpgradeOrchestrator
	dispatcher *AgentDispatcher
	assigner   *Assigner

	history *HistoryStore // 可选
	sampler *Sampler      // 可选

	httpSrv *http.Server
	stopCh  chan struct{}
}

func NewAdminServer(cfg Config) (*AdminServer, error) {
	s := &AdminServer{cfg: cfg, stopCh: make(chan struct{})}

	// 1. TaskStore
	tasks, err := NewTaskStore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("init task store: %w", err)
	}
	s.tasks = tasks

	// 2. AgentRegistry
	s.agents = NewAgentRegistry(cfg.AgentRegistry, nil)

	// 3. BinaryStore
	binaries, err := NewBinaryStore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("init binary store: %w", err)
	}
	s.binaries = binaries

	// 4. MetricsAggregator
	s.aggregator = NewMetricsAggregator(s.agents)

	// 5. AgentDispatcher
	s.dispatcher = NewAgentDispatcher()

	// 6. Assigner
	s.assigner = NewAssigner()

	// 7. UpgradeOrchestrator
	s.upgrader = NewUpgradeOrchestrator(
		s.agents, s.binaries, s.dispatcher, cfg.PublicURL,
		parseDurationDefault(cfg.Upgrade.RolloutDelay, 5*time.Second),
		parseDurationDefault(cfg.Upgrade.PerAgentTimeout, 5*time.Minute),
	)

	// 8. HistoryStore（可选）
	if cfg.History.Enabled {
		history, err := NewHistoryStore(cfg.History)
		if err != nil {
			return nil, fmt.Errorf("init history store: %w", err)
		}
		s.history = history

		sampler := NewSampler(
			parseDurationDefault(cfg.History.SamplerInterval, 10*time.Second),
			s.aggregator, s.history, s.agents,
		)
		s.sampler = sampler
	}

	// 9. 终态回调
	s.tasks.SetOnTerminal(s.onTaskTerminal)

	return s, nil
}

// Run 启动 Admin 服务器（阻塞）。
func (s *AdminServer) Run() error {
	// 初始化协程池
	utils.InitWorkPool(nil)

	// 启动心跳检测
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.agents.StartHealthChecker(ctx)

	// 启动定时清理（可选）
	if s.history != nil {
		s.history.StartPruneLoop(ctx)
	}

	// 注册路由
	mux := s.registerRoutes()
	s.httpSrv = &http.Server{
		Addr:    s.cfg.ListenAddr,
		Handler: mux,
	}

	stresslog.Info("admin 启动",
		zap.String("addr", s.cfg.ListenAddr),
		zap.Bool("history", s.cfg.History.Enabled))

	// 信号处理
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		stresslog.Info("收到退出信号，开始关闭...")
		s.Shutdown(context.Background())
	}()

	if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// Shutdown 优雅关闭。
func (s *AdminServer) Shutdown(ctx context.Context) error {
	if s.httpSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		s.httpSrv.Shutdown(shutdownCtx)
	}
	if s.history != nil {
		s.history.Close()
	}
	utils.GetWorkPool().Shutdown()
	close(s.stopCh)
	return nil
}

func (s *AdminServer) onTaskTerminal(task *Task) {
	// 停止 Sampler
	if s.sampler != nil {
		s.sampler.Stop(task.ID)
	}

	// 异步归档
	if s.history == nil {
		return
	}
	taskID := task.ID
	utils.GetWorkPool().Go(func() {
		finalStress := s.aggregator.AggregateStress(taskID)
		finalSys := s.aggregator.AggregateSystem()
		if err := s.history.Archive(task, finalStress, finalSys); err != nil {
			stresslog.Error("任务归档失败",
				zap.String("taskId", taskID),
				zap.Error(err))
		}
	})
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
