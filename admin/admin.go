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

	// 3. MetricsAggregator
	s.aggregator = NewMetricsAggregator(s.agents)

	// 4. AgentDispatcher
	s.dispatcher = NewAgentDispatcher()

	// 5. Assigner
	s.assigner = NewAssigner()

	// 6. HistoryStore（可选）
	//
	// 启动期一定要"显式"打日志说明 history 模块状态，否则运维只能从启动总日志里
	// 那一笔 zap.Bool("history", false) 推断，极易忽略；前端按"历史"按钮才弹
	// HISTORY_DISABLED 时也会一头雾水。三个分支都要可观察：
	//   - enabled=true 且成功 → NewHistoryStore 内部已打 Info "HistoryStore 已连接 MySQL"；
	//   - enabled=true 但失败 → return err，admin 启动失败（fail-fast）；
	//   - enabled=false → 这里 Warn 一笔，提示如何启用。
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
	} else {
		stresslog.Warn("[ADMIN] history 模块未启用：所有 /api/history* 接口将返回 HISTORY_DISABLED；" +
			"如需启用，请在 config.json 设置 history.enabled=true 且填写 history.mysql.dsn")
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
		// finalStress 来源优先级：
		//   1. task.Reports[*].FinalSnapshot —— agent 终止时主动上报，确定且完整；
		//   2. aggregator.AggregateStress(taskID) —— 兜底，但任务终止后 agent 心跳通常已把
		//      CurrentTaskID 清成空字符串，AggregateStress 会过滤掉所有 agent 返回空快照，
		//      所以这里只用作历史路径（Reports 缺失时的次优选择）。
		// 不切换到 aggregator 主路径的原因：测试时归档常拿到空 actions / 0 connections，
		// 用户在历史详情里只看到一片空白，体验非常差。
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

// buildFinalStressFromReports 从已落地的 agent 终止报告聚合最终压测快照。
// 终止报告由 agent 在停 robot 之后主动 POST 给 admin（POST /api/internal/agents/:id/tasks/:tid/report），
// 此时 task 的所有 agent 都已经把自己的 CollectorSnapshot 序列化进 report.FinalSnapshot，
// 不依赖 agent 心跳里的 CurrentTaskID，是终态最可信的来源。
func buildFinalStressFromReports(task *Task) *monitor.CollectorSnapshot {
	if task == nil || len(task.Reports) == 0 {
		return nil
	}
	snaps := make([]*monitor.CollectorSnapshot, 0, len(task.Reports))
	for _, r := range task.Reports {
		// FinalSnapshot 是值类型，零值（Uptime=0 且 Actions=nil）也算"agent 没采到任何数据"，
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
