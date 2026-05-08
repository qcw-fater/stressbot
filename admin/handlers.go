package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"stressbot/monitor"
	"stressbot/utils"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// registerRoutes 注册所有 HTTP 路由。
func (s *AdminServer) registerRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// ── Agent 上行 ──
	mux.HandleFunc("POST /api/agent/register", s.handleAgentRegister)
	mux.HandleFunc("POST /api/agent/{id}/heartbeat", s.handleAgentHeartbeat)
	mux.HandleFunc("POST /api/agent/{id}/deregister", s.handleAgentDeregister)
	mux.HandleFunc("POST /api/agent/stress", s.handleAgentStressReport)
	mux.HandleFunc("POST /api/agent/system", s.handleAgentSystemReport)
	mux.HandleFunc("POST /api/agent/{id}/task/{tid}/done", s.handleAgentTaskDone)
	mux.HandleFunc("GET /api/agent/{id}/pending-task", s.handleAgentPendingTask)

	// ── 前端-任务 ──
	mux.HandleFunc("POST /api/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /api/tasks", s.handleListTasks)
	mux.HandleFunc("GET /api/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("GET /api/tasks/{id}/config/{path...}", s.handleGetTaskConfig)
	mux.HandleFunc("POST /api/tasks/{id}/start", s.handleStartTask)
	mux.HandleFunc("POST /api/tasks/{id}/stop", s.handleStopTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.handleDeleteTask)

	// ── 前端-Agent ──
	mux.HandleFunc("GET /api/agents", s.handleListAgents)
	mux.HandleFunc("GET /api/agents/{id}", s.handleGetAgent)
	mux.HandleFunc("DELETE /api/agents/{id}", s.handleDeleteAgent)

	// ── 前端-指标 ──
	mux.HandleFunc("GET /api/metrics", s.handleGetMetrics)
	mux.HandleFunc("GET /api/metrics/summary", s.handleGetMetricsSummary)
	mux.HandleFunc("GET /api/metrics/agents", s.handleGetAgentMetrics)
	mux.HandleFunc("GET /api/metrics/agents/{id}", s.handleGetSingleAgentMetrics)
	mux.HandleFunc("GET /api/system", s.handleGetSystem)
	mux.HandleFunc("GET /api/system/agents", s.handleGetSystemAgents)
	mux.HandleFunc("GET /api/system/agents/{id}", s.handleGetSystemAgent)

	// ── 历史归档 ──
	mux.HandleFunc("GET /api/history", s.handleListHistory)
	mux.HandleFunc("GET /api/history/tags", s.handleGetHistoryTags)
	mux.HandleFunc("GET /api/history/{id}", s.handleGetHistory)
	mux.HandleFunc("PUT /api/history/{id}", s.handleUpdateHistory)
	mux.HandleFunc("DELETE /api/history/{id}", s.handleDeleteHistory)
	mux.HandleFunc("GET /api/history/{id}/agents", s.handleGetHistoryAgents)
	mux.HandleFunc("GET /api/history/{id}/config", s.handleGetHistoryConfig)
	mux.HandleFunc("GET /api/history/{id}/timeseries", s.handleGetHistoryTimeseries)
	mux.HandleFunc("POST /api/history/{id}/clone", s.handleCloneHistory)
	mux.HandleFunc("GET /api/history/compare", s.handleCompareHistory)

	// ── 静态资源 ──
	if s.cfg.StaticDir != "" {
		fs := http.FileServer(http.Dir(s.cfg.StaticDir))
		mux.Handle("/", fs)
	}

	return mux
}

// ── Agent 上行 ──

func (s *AdminServer) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("invalid json"))
		return
	}
	if req.AgentID == "" {
		writeError(w, ErrInvalidArgument.WithMessage("agentId required"))
		return
	}

	node := &AgentNode{
		ID:             req.AgentID,
		Name:           req.Name,
		Address:        req.Address,
		AppVersion:     req.AppVersion,
		MaxBots:        req.MaxBots,
		StressInterval: req.StressInterval,
		SystemInterval: req.SystemInterval,
		StaticInfo:     req.StaticInfo,
		Status:         AgentIdle,
		LastHeartbeatAt: time.Now(),
	}
	if err := s.agents.Register(node); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, RegisterResponse{
		AgentID:        req.AgentID,
		HeartbeatTTL:   s.cfg.AgentRegistry.UnhealthyAfter,
		StressEndpoint: "/api/agent/stress",
		SystemEndpoint: "/api/agent/system",
	})
}

func (s *AdminServer) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	var req HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, ErrInvalidArgument)
		return
	}
	req.AgentID = agentID

	if err := s.agents.Heartbeat(agentID, req); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *AdminServer) handleAgentDeregister(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if err := s.agents.Deregister(agentID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAgentStressReport 丢弃过期 stress 报告，避免跨任务串数据。
func (s *AdminServer) handleAgentStressReport(w http.ResponseWriter, r *http.Request) {
	var report StressReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeError(w, ErrInvalidArgument)
		return
	}
	if agent, ok := s.agents.Get(report.AgentID); ok {
		if agent.CurrentTaskID == "" || (report.TaskID != "" && report.TaskID != agent.CurrentTaskID) {
			// 旧任务的延迟报告 / agent 已 idle，直接丢弃，避免 LatestStress 被串。
			stresslog.Debug("丢弃过期 stress 报告",
				zap.String("agentId", report.AgentID),
				zap.String("reportTaskId", report.TaskID),
				zap.String("currentTaskId", agent.CurrentTaskID))
			writeJSON(w, http.StatusOK, map[string]string{"status": "stale"})
			return
		}
	}
	reportedAt := report.ReportedAt
	if reportedAt.IsZero() {
		reportedAt = time.Now()
	}
	s.agents.UpdateStress(report.AgentID, report.Snapshot, reportedAt)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *AdminServer) handleAgentSystemReport(w http.ResponseWriter, r *http.Request) {
	var report SystemReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeError(w, ErrInvalidArgument)
		return
	}
	reportedAt := report.ReportedAt
	if reportedAt.IsZero() {
		reportedAt = time.Now()
	}
	s.agents.UpdateSystem(report.AgentID, &report.Snapshot, reportedAt)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *AdminServer) handleAgentTaskDone(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	taskID := r.PathValue("tid")

	var report TaskCompletionReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeError(w, ErrInvalidArgument)
		return
	}
	report.AgentID = agentID
	report.TaskID = taskID

	var needTransition TaskState // 零值表示不需要转换
	err := s.tasks.Update(taskID, func(t *Task) {
		if t.Reports == nil {
			t.Reports = make(map[string]TaskCompletionReport)
		}
		t.Reports[agentID] = report

		s.agents.Heartbeat(agentID, HeartbeatRequest{
			AgentID: agentID,
			Status:  "idle",
		})

		// 检查是否全部完成（自然完成: running→stopped，手动停止: stopping→stopped）
		if len(t.Reports) == len(t.Assignments) {
			if t.State == TaskRunning {
				needTransition = TaskRunning
			} else if t.State == TaskStopping {
				needTransition = TaskStopping
			}
		}
	})
	if err != nil {
		writeError(w, err)
		return
	}

	// Transition 必须在 Update 外部调用，避免死锁（两者都拿 ts.mu）
	if needTransition == TaskRunning {
		s.tasks.Transition(taskID, TaskRunning, TaskStopped)
	} else if needTransition == TaskStopping {
		s.tasks.Transition(taskID, TaskStopping, TaskStopped)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *AdminServer) handleAgentPendingTask(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	node, ok := s.agents.Get(agentID)
	if !ok {
		writeError(w, ErrAgentNotFound)
		return
	}
	if node.CurrentTaskID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"taskId": node.CurrentTaskID,
	})
}

// ── 前端-任务 ──

// handleCreateTask multipart/form-data 创建任务。
func (s *AdminServer) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("multipart parse error"))
		return
	}

	name := r.FormValue("name")
	totalBotsStr := r.FormValue("totalBots")
	if name == "" || totalBotsStr == "" {
		writeError(w, ErrInvalidArgument.WithMessage("name and totalBots required"))
		return
	}
	totalBots, err := strconv.Atoi(totalBotsStr)
	if err != nil || totalBots <= 0 {
		writeError(w, ErrInvalidArgument.WithMessage("totalBots must be positive integer"))
		return
	}

	var cfg TaskConfig

	// flow.json（必需）
	flowFile, _, err := r.FormFile("flow.json")
	if err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("flow.json file required"))
		return
	}
	flowData, _ := io.ReadAll(flowFile)
	flowFile.Close()
	cfg.FlowJSON = json.RawMessage(flowData)

	// header.json（可选）
	if headerFile, _, err := r.FormFile("header.json"); err == nil {
		headerData, _ := io.ReadAll(headerFile)
		headerFile.Close()
		cfg.HeaderJSON = json.RawMessage(headerData)
	}

	// proto 文件
	cfg.ProtoFiles = make(map[string][]byte)
	for key, files := range r.MultipartForm.File {
		if strings.HasPrefix(key, "proto/") || key == "proto" {
			for _, fh := range files {
				f, err := fh.Open()
				if err != nil {
					continue
				}
				data, _ := io.ReadAll(f)
				f.Close()
				var fileName string
				if strings.HasPrefix(key, "proto/") && key != "proto/" {
					fileName = strings.TrimPrefix(key, "proto/")
				} else {
					fileName = fh.Filename
				}
				cfg.ProtoFiles[fileName] = data
			}
		}
	}

	// lua 脚本
	cfg.LuaScripts = make(map[string][]byte)
	for key, files := range r.MultipartForm.File {
		if strings.HasPrefix(key, "scripts/") || key == "scripts" {
			for _, fh := range files {
				f, err := fh.Open()
				if err != nil {
					continue
				}
				data, _ := io.ReadAll(f)
				f.Close()
				var fileName string
				if strings.HasPrefix(key, "scripts/") && key != "scripts/" {
					fileName = strings.TrimPrefix(key, "scripts/")
				} else {
					fileName = fh.Filename
				}
				cfg.LuaScripts[fileName] = data
			}
		}
	}

	// robotConfig（JSON string）
	if rc := r.FormValue("robotConfig"); rc != "" {
		_ = json.Unmarshal([]byte(rc), &cfg.RobotConfig)
	}

	// deadline（可选）
	if dl := r.FormValue("deadline"); dl != "" {
		if t, err := time.Parse(time.RFC3339, dl); err == nil {
			cfg.Deadline = &t
		}
	}

	task := &Task{
		ID:        generateID(),
		Name:      name,
		State:     TaskPending,
		TotalBots: totalBots,
		Config:    cfg,
		CreatedAt: time.Now(),
	}
	if err := s.tasks.Create(task); err != nil {
		writeError(w, err)
		return
	}

	stresslog.Info("任务创建", zap.String("taskId", task.ID), zap.String("name", task.Name))
	writeJSON(w, http.StatusCreated, map[string]string{"id": task.ID})
}

func (s *AdminServer) handleListTasks(w http.ResponseWriter, r *http.Request) {
	tasks := s.tasks.List()

	// 过滤
	stateFilter := r.URL.Query().Get("state")
	if stateFilter != "" {
		filtered := make([]*Task, 0, len(tasks))
		for _, t := range tasks {
			if string(t.State) == stateFilter {
				filtered = append(filtered, t)
			}
		}
		tasks = filtered
	}

	total := len(tasks)

	// 分页
	offset := parseIntOrDefault(r.URL.Query().Get("offset"), 0)
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 0) // 0 = 不限制
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}

	// 构建 TaskBrief
	items := make([]map[string]any, 0, end-offset)
	for i := offset; i < end; i++ {
		t := tasks[i]
		brief := map[string]any{
			"id":         t.ID,
			"name":       t.Name,
			"state":      t.State,
			"totalBots":  t.TotalBots,
			"agentCount": len(t.Assignments),
			"createdAt":  t.CreatedAt,
		}
		if t.StartedAt != nil {
			brief["startedAt"] = *t.StartedAt
		}
		if t.StoppedAt != nil {
			brief["stoppedAt"] = *t.StoppedAt
		}
		items = append(items, brief)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total": total,
		"items": items,
	})
}

func (s *AdminServer) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, ok := s.tasks.Get(id)
	if !ok {
		writeError(w, ErrTaskNotFound)
		return
	}

	writeJSON(w, http.StatusOK, task)
}

// handleGetTaskConfig 任务配置文件下载（Agent 用）。
func (s *AdminServer) handleGetTaskConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path := r.PathValue("path")

	task, ok := s.tasks.Get(id)
	if !ok {
		writeError(w, ErrTaskNotFound)
		return
	}

	switch path {
	case "flow.json":
		if task.Config.FlowJSON == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(task.Config.FlowJSON)

	case "header.json":
		if task.Config.HeaderJSON == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(task.Config.HeaderJSON)

	case "config.json":
		// Agent 运行时配置（robotConfig + 超时等）
		configJSON, _ := json.Marshal(map[string]any{
			"authAddr":    task.Config.RobotConfig.AuthAddr,
			"concurrency": task.Config.RobotConfig.Concurrency,
			"timeoutSec":  task.Config.RobotConfig.TimeoutSec,
			"deadline":    task.Config.Deadline,
		})
		w.Header().Set("Content-Type", "application/json")
		w.Write(configJSON)

	default:
		// proto 文件或 lua 脚本
		if data, found := task.Config.ProtoFiles[path]; found {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(data)
			return
		}
		if data, found := task.Config.LuaScripts[path]; found {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(data)
			return
		}
		// 尝试去掉 proto/ 或 scripts/ 前缀匹配
		baseName := path
		if idx := strings.Index(path, "/"); idx >= 0 {
			baseName = path[idx+1:]
		}
		if data, found := task.Config.ProtoFiles[baseName]; found {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(data)
			return
		}
		if data, found := task.Config.LuaScripts[baseName]; found {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}
}

func (s *AdminServer) handleStartTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	task, err := s.tasks.StartTask(id)
	if err != nil {
		writeError(w, err)
		return
	}

	// 分配 Agent
	idleAgents := s.agents.ListByStatus(AgentIdle)
	assignments, err := s.assigner.Assign(task, idleAgents, task.Config.RobotConfig.StartNumber)
	if err != nil {
		s.tasks.Transition(id, TaskStarting, TaskFailed)
		writeError(w, err)
		return
	}

	// 保存分配方案
	s.tasks.Update(id, func(t *Task) {
		t.Assignments = assignments
	})

	// 启动 Sampler
	if s.sampler != nil {
		_ = s.sampler.Start(id)
	}

	// 异步推送任务到各 Agent（捕获值，避免 data race）
	taskID := task.ID
	taskName := task.Name
	utils.GetWorkPool().Go(func() { s.startTaskBackground(taskID, taskName, assignments) })

	writeJSON(w, http.StatusAccepted, map[string]any{
		"taskId":      id,
		"assignments": assignments,
	})
}

func (s *AdminServer) startTaskBackground(taskID, taskName string, assignments []Assignment) {
	var failed []string
	var succeeded []string

	// 读取任务配置填充 TaskAssignment
	task, _ := s.tasks.Get(taskID)
	var rc RobotConfig
	if task != nil {
		rc = task.Config.RobotConfig
	}

	// 构建配置文件清单
	var configFiles []string
	if task != nil {
		if task.Config.FlowJSON != nil {
			configFiles = append(configFiles, "flow.json")
		}
		if task.Config.HeaderJSON != nil {
			configFiles = append(configFiles, "header.json")
		}
		for name := range task.Config.ProtoFiles {
			configFiles = append(configFiles, "proto/"+name)
		}
		for name := range task.Config.LuaScripts {
			configFiles = append(configFiles, "scripts/"+name)
		}
	}

	for _, a := range assignments {
		agent, ok := s.agents.Get(a.AgentID)
		if !ok {
			failed = append(failed, a.AgentID)
			continue
		}

		cfg := TaskAssignment{
			TaskID:            taskID,
			TaskName:          taskName,
			StartNumber:       a.StartNumber,
			TotalBots:         a.TotalBots,
			AccountPrefix:     stringOr(rc.AccountPrefix, "bot_"),
			MainService:       stringOr(rc.MainService, "logic"),
			AuthAddress:       rc.AuthAddr,
			AuthExtra:         rc.AuthExtra,
			ConcurrentNum:     rc.Concurrency,
			HeartbeatInterval: secsOr(rc.HeartbeatSec, 5),
			TCPTimeout:        secsOr(rc.TimeoutSec, 60),
			HTTPTimeout:       secsOr(rc.HTTPTimeoutSec, 10),
			ApdexT:            intOr(rc.ApdexT, 100),
			LogLevel:          rc.LogLevel,
			ConfigURL:         fmt.Sprintf("%s/api/tasks/%s/config", s.cfg.PublicURL, taskID),
			ConfigFiles:       configFiles,
		}

		if err := s.dispatcher.AssignTask(agent.Address, cfg); err != nil {
			failed = append(failed, a.AgentID)
			stresslog.Error("推送任务失败",
				zap.String("agentId", a.AgentID),
				zap.Error(err))
		} else {
			succeeded = append(succeeded, a.AgentID)
			s.agents.Heartbeat(a.AgentID, HeartbeatRequest{
				AgentID:       a.AgentID,
				Status:        "busy",
				CurrentTaskID: taskID,
				CurrentBots:   a.TotalBots,
			})
		}
	}

	if len(failed) > 0 {
		// 向已接受任务的 Agent 发送 stop，回收资源
		for _, agentID := range succeeded {
			agent, ok := s.agents.Get(agentID)
			if !ok || agent.Status == AgentOffline {
				continue
			}
			if err := s.dispatcher.Stop(agent.Address, taskID); err != nil {
				stresslog.Warn("回收任务失败",
					zap.String("agentId", agentID),
					zap.Error(err))
			}
		}

		s.tasks.Transition(taskID, TaskStarting, TaskFailed)
		if s.sampler != nil {
			s.sampler.Stop(taskID)
		}
		stresslog.Error("任务启动失败",
			zap.String("taskId", taskID),
			zap.Strings("failedAgents", failed))
		return
	}

	s.tasks.Transition(taskID, TaskStarting, TaskRunning)
	stresslog.Info("任务启动成功",
		zap.String("taskId", taskID),
		zap.Int("agents", len(assignments)))
}

func (s *AdminServer) handleStopTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	task, ok := s.tasks.Get(id)
	if !ok {
		writeError(w, ErrTaskNotFound)
		return
	}
	if task.State != TaskRunning {
		writeError(w, ErrTaskInvalidState.WithMessage(fmt.Sprintf("task is %s, expected running", task.State)))
		return
	}

	if _, err := s.tasks.Transition(id, TaskRunning, TaskStopping); err != nil {
		writeError(w, err)
		return
	}

	for _, a := range task.Assignments {
		agent, ok := s.agents.Get(a.AgentID)
		if !ok || agent.Status == AgentOffline {
			continue
		}
		if err := s.dispatcher.Stop(agent.Address, id); err != nil {
			stresslog.Warn("停止命令发送失败",
				zap.String("agentId", a.AgentID),
				zap.Error(err))
		}
	}

	updated, _ := s.tasks.Get(id)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":         id,
		"name":       updated.Name,
		"state":      updated.State,
		"totalBots":  updated.TotalBots,
		"agentCount": len(updated.Assignments),
		"createdAt":  updated.CreatedAt,
	})
}

func (s *AdminServer) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.Delete(id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── 前端-Agent ──

func (s *AdminServer) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.agents.List()

	items := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		brief := map[string]any{
			"agentId":          a.ID,
			"name":             a.Name,
			"address":          a.Address,
			"appVersion":       a.AppVersion,
			"maxBots":          a.MaxBots,
			"status":           a.Status,
			"currentTaskId":    a.CurrentTaskID,
			"currentBots":      a.CurrentBots,
			"staticInfo":       a.StaticInfo,
			"lastHeartbeatAt":  a.LastHeartbeatAt,
		}
		if !a.StressUpdatedAt.IsZero() {
			brief["stressUpdatedAt"] = a.StressUpdatedAt
		}
		if !a.SystemUpdatedAt.IsZero() {
			brief["systemUpdatedAt"] = a.SystemUpdatedAt
		}
		// 系统指标摘要
		if a.LatestSystem != nil {
			brief["cpuPercent"] = a.LatestSystem.CPUPercent
			brief["memPercent"] = a.LatestSystem.MemPercent
			brief["numGoroutine"] = a.LatestSystem.NumGoroutine
		}
		items = append(items, brief)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
	})
}

func (s *AdminServer) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, ok := s.agents.Get(id)
	if !ok {
		writeError(w, ErrAgentNotFound)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *AdminServer) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, ok := s.agents.Get(id)
	if !ok {
		writeError(w, ErrAgentNotFound)
		return
	}
	if agent.Status != AgentOffline {
		writeError(w, ErrAgentBusy.WithMessage("can only delete offline agents"))
		return
	}
	if err := s.agents.Deregister(id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── 前端-指标 ──

func (s *AdminServer) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	active := s.tasks.ActiveTask()
	if active == nil {
		writeJSON(w, http.StatusOK, &monitor.CollectorSnapshot{})
		return
	}
	snap := s.aggregator.AggregateStress(active.ID)
	writeJSON(w, http.StatusOK, snap)
}

// handleGetMetricsSummary 文本摘要。
func (s *AdminServer) handleGetMetricsSummary(w http.ResponseWriter, r *http.Request) {
	active := s.tasks.ActiveTask()
	if active == nil {
		writeJSON(w, http.StatusOK, map[string]string{"summary": "no active task"})
		return
	}

	snap := s.aggregator.AggregateStress(active.ID)
	var b strings.Builder
	fmt.Fprintf(&b, "Task: %s (%s)\n", active.Name, active.ID)
	fmt.Fprintf(&b, "Total Actions: %d\n", snap.TotalActions)
	if len(snap.Actions) > 0 {
		for _, a := range snap.Actions {
			fmt.Fprintf(&b, "  %s: count=%d success=%.1f%% p50=%.1fms p99=%.1fms\n",
				a.Name, a.SampleCount, a.SuccessRate*100, a.Latency.P50Ms, a.Latency.P99Ms)
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(b.String()))
}

func (s *AdminServer) handleGetAgentMetrics(w http.ResponseWriter, r *http.Request) {
	agents := s.agents.List()
	items := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		if a.LatestStress != nil {
			items = append(items, map[string]any{
				"agentId":   a.ID,
				"agentName": a.Name,
				"snapshot":  a.LatestStress,
				"updatedAt": a.StressUpdatedAt,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
	})
}

func (s *AdminServer) handleGetSingleAgentMetrics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, ok := s.agents.Get(id)
	if !ok {
		writeError(w, ErrAgentNotFound)
		return
	}
	if agent.LatestStress == nil {
		writeJSON(w, http.StatusOK, map[string]string{"message": "no stress data"})
		return
	}
	writeJSON(w, http.StatusOK, agent.LatestStress)
}

func (s *AdminServer) handleGetSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.aggregator.AggregateSystem()
	writeJSON(w, http.StatusOK, snap)
}

func (s *AdminServer) handleGetSystemAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.agents.List()
	now := time.Now()
	items := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		if a.Status == AgentOffline {
			continue
		}
		item := map[string]any{
			"agentId":   a.ID,
			"agentName": a.Name,
			"status":    a.Status,
		}
		if a.LatestSystem != nil {
			item["snapshot"] = a.LatestSystem
			item["updatedAt"] = a.SystemUpdatedAt
			item["isStale"] = now.Sub(a.SystemUpdatedAt) > 30*time.Second
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
	})
}

func (s *AdminServer) handleGetSystemAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, ok := s.agents.Get(id)
	if !ok {
		writeError(w, ErrAgentNotFound)
		return
	}
	if agent.LatestSystem == nil {
		writeJSON(w, http.StatusOK, map[string]string{"message": "no system data"})
		return
	}
	writeJSON(w, http.StatusOK, agent.LatestSystem)
}

// ── 工具函数 ──

func parseIntOrDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func parseBoolOrDefault(s string, def bool) bool {
	if s == "" {
		return def
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return b
}

func parseTimeOrDefault(s string, def time.Time) time.Time {
	if s == "" {
		return def
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return def
	}
	return t
}

func parseTagsFromQuery(r *http.Request, key string) []string {
	var tags []string
	for _, t := range r.URL.Query()[key] {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}
