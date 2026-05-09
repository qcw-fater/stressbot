package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"stressbot/monitor"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// AdminServer Admin HTTP 服务器。
type AdminServer struct {
	cfg Config

	tasks      *TaskStore
	agents     *AgentRegistry
	aggregator *MetricsAggregator
	dispatcher *AgentDispatcher
	assigner   *Assigner

	logsProxyClient *http.Client // Agent 日志代理（5s 超时）

	history *HistoryStore // 可选
	sampler *Sampler      // 可选

	httpSrv *http.Server
	stopCh  chan struct{}
	wg      sync.WaitGroup
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

	// 3. MetricsAggregator
	s.aggregator = NewMetricsAggregator(s.agents)

	// 4. AgentDispatcher
	s.dispatcher = NewAgentDispatcher()

	// 5. Logs proxy client
	s.logsProxyClient = &http.Client{Timeout: 5 * time.Second}

	// 6. Assigner
	s.assigner = NewAssigner()

	// 7. HistoryStore（可选）
	if cfg.History.Enabled {
		history, err := NewHistoryStore(cfg.History)
		if err != nil {
			return nil, fmt.Errorf("init history store: %w", err)
		}
		s.history = history

		sampler := NewSampler(
			utils.ParseDurationDefault(cfg.History.SamplerInterval, 10*time.Second),
			s.aggregator, s.history, s.agents,
		)
		s.sampler = sampler
	} else {
		stresslog.Info("[ADMIN] history 模块未启用：所有 /api/history* 接口将返回 HISTORY_DISABLED；" +
			"如需启用，请在 config.json 设置 history.enabled=true 且填写 history.mysql.dsn")
	}

	// 8. 终态回调
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
		// 优先用 agent 终止报告聚合，兜底用心跳聚合
		finalStress := buildFinalStressFromReports(task)
		if finalStress == nil || len(finalStress.Actions) == 0 {
			finalStress = s.aggregator.AggregateStress(taskID)
		}
		finalSys := s.aggregator.AggregateSystem()
		if err := s.history.Archive(task, finalStress, finalSys); err != nil {
			stresslog.Error("任务归档失败",
				zap.String("taskId", taskID),
				zap.Error(err))
		}
	})
}

// buildFinalStressFromReports 从 agent 终止报告聚合最终快照（优先于心跳聚合）。
func buildFinalStressFromReports(task *Task) *monitor.CollectorSnapshot {
	if task == nil || len(task.Reports) == 0 {
		return nil
	}
	snaps := make([]*monitor.CollectorSnapshot, 0, len(task.Reports))
	for _, r := range task.Reports {
		// 过滤零值快照
		// 这种条目并入 MergeSnapshots 没意义，过滤掉。
		if r.FinalSnapshot.UptimeSec == 0 && len(r.FinalSnapshot.Actions) == 0 {
			continue
		}
		snap := r.FinalSnapshot // 复制一份避免外部修改原 map
		snaps = append(snaps, &snap)
	}
	if len(snaps) == 0 {
		return nil
	}
	return monitor.MergeSnapshots(snaps)
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
