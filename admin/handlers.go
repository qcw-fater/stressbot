package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"stressbot/errcode"
	"stressbot/logview"
	"stressbot/monitor"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// registerRoutes 注册所有 HTTP 路由。
//
// 顶层用 recoverMiddleware 包裹，确保 handler panic 不会断开连接，
// 而是返回标准 500 JSON 并把 stack trace 写入应用日志。
func (s *AdminServer) registerRoutes() http.Handler {
	mux := http.NewServeMux()

	// ── Agent 上行 ──
	mux.HandleFunc("POST /sbot/agent/register", s.handleAgentRegister)
	mux.HandleFunc("POST /sbot/agent/{id}/heartbeat", s.handleAgentHeartbeat)
	mux.HandleFunc("POST /sbot/agent/{id}/deregister", s.handleAgentDeregister)
	mux.HandleFunc("POST /sbot/agent/stress", s.handleAgentStressReport)
	mux.HandleFunc("POST /sbot/agent/system", s.handleAgentSystemReport)
	mux.HandleFunc("POST /sbot/agent/{id}/task/{tid}/done", s.handleAgentTaskDone)
	mux.HandleFunc("GET /sbot/agent/{id}/pending-task", s.handleAgentPendingTask)

	// ── 前端-资源基线 ──
	mux.HandleFunc("POST /sbot/resources/baseline", s.handleUpdateBaseline)

	// ── 前端-任务 ──
	mux.HandleFunc("POST /sbot/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /sbot/tasks", s.handleListTasks)
	mux.HandleFunc("GET /sbot/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("GET /sbot/tasks/{id}/config/{path...}", s.handleGetTaskConfig)
	mux.HandleFunc("POST /sbot/tasks/{id}/start", s.handleStartTask)
	mux.HandleFunc("POST /sbot/tasks/{id}/stop", s.handleStopTask)
	mux.HandleFunc("DELETE /sbot/tasks/{id}", s.handleDeleteTask)

	// ── 前端-Agent ──
	mux.HandleFunc("GET /sbot/agents", s.handleListAgents)
	mux.HandleFunc("GET /sbot/agents/{id}", s.handleGetAgent)
	mux.HandleFunc("DELETE /sbot/agents/{id}", s.handleDeleteAgent)
	mux.HandleFunc("POST /sbot/agents/{id}/shutdown", s.handleShutdownAgent)
	mux.HandleFunc("POST /sbot/agents/shutdown-all", s.handleShutdownAllAgents)

	// ── 前端-指标 ──
	mux.HandleFunc("GET /sbot/metrics", s.handleGetMetrics)
	mux.HandleFunc("GET /sbot/metrics/summary", s.handleGetMetricsSummary)
	mux.HandleFunc("GET /sbot/metrics/agents", s.handleGetAgentMetrics)
	mux.HandleFunc("GET /sbot/metrics/agents/{id}", s.handleGetSingleAgentMetrics)
	mux.HandleFunc("GET /sbot/system", s.handleGetSystem)
	mux.HandleFunc("GET /sbot/system/agents", s.handleGetSystemAgents)
	mux.HandleFunc("GET /sbot/system/agents/{id}", s.handleGetSystemAgent)

	// ── 历史归档 ──
	mux.HandleFunc("GET /sbot/history", s.handleListHistory)
	mux.HandleFunc("GET /sbot/history/tags", s.handleGetHistoryTags)
	mux.HandleFunc("GET /sbot/history/{id}", s.handleGetHistory)
	mux.HandleFunc("PUT /sbot/history/{id}", s.handleUpdateHistory)
	mux.HandleFunc("DELETE /sbot/history/{id}", s.handleDeleteHistory)
	mux.HandleFunc("GET /sbot/history/{id}/agents", s.handleGetHistoryAgents)
	mux.HandleFunc("GET /sbot/history/{id}/config", s.handleGetHistoryConfig)
	mux.HandleFunc("GET /sbot/history/{id}/timeseries", s.handleGetHistoryTimeseries)
	mux.HandleFunc("POST /sbot/history/{id}/clone", s.handleCloneHistory)
	mux.HandleFunc("GET /sbot/history/compare", s.handleCompareHistory)

	// ── 日志 ──
	mux.HandleFunc("GET /sbot/logs/admin", s.handleGetAdminLogs)
	mux.HandleFunc("GET /sbot/logs/agents/{id}", s.handleGetAgentLogs)
	mux.HandleFunc("GET /sbot/logs/admin/files", s.handleListAdminLogFiles)
	mux.HandleFunc("GET /sbot/logs/admin/files/{name}", s.handleDownloadAdminLogFile)
	mux.HandleFunc("GET /sbot/logs/agents/{id}/files", s.handleListAgentLogFiles)
	mux.HandleFunc("GET /sbot/logs/agents/{id}/files/{name}", s.handleDownloadAgentLogFile)

	// ── 基线资源读取 ──
	mux.HandleFunc("GET /sbot/baseline/proto/index.json", s.handleBaselineProtoIndex)
	mux.HandleFunc("GET /sbot/baseline/proto/{name}", s.handleBaselineProtoFile)
	mux.HandleFunc("GET /sbot/baseline/scripts/index.json", s.handleBaselineScriptIndex)
	mux.HandleFunc("GET /sbot/baseline/scripts/{name}", s.handleBaselineScriptFile)
	mux.HandleFunc("GET /sbot/baseline/adapter/codec.lua", s.handleBaselineAdapter)
	mux.HandleFunc("GET /sbot/baseline/adapter/error.lua", s.handleBaselineErrorMap)
	mux.HandleFunc("GET /sbot/baseline/flow/flow.json", s.handleBaselineFlow)
	mux.HandleFunc("GET /sbot/baseline/config.json", s.handleBaselineConfig)

	// ── 错误码 ──
	mux.HandleFunc("GET /sbot/api/error-codes", s.handleErrorCodeIndex)

	// ── 静态资源 ──
	fs := http.FileServer(http.Dir(s.cfg.StaticDir))
	mux.Handle("/", fs)

	return recoverMiddleware(mux)
}

// recoverMiddleware 捕获 handler panic 并写入应用日志，返回标准 500 JSON。
// 避免依赖 net/http 默认 per-request recover（仅写 stderr，且会直接断开连接）。
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stresslog.Error("[ADMIN] HTTP handler panic",
					zap.String("path", r.URL.Path),
					zap.String("method", r.Method),
					zap.Any("panic", rec),
					zap.String("stack", string(debug.Stack())))
				writeError(w, ErrInternal.WithMessage("internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ── Agent 上行 ──

// handleAgentRegister 处理 Agent 注册。
//
// 用户需求 §5：运行任务期间允许 Agent 注册，因为 Agent 异常重启走重连流程
// 等同于"新注册"；不允许会导致行为不一致。
//
// 用户需求 §2.2：Agent 进程重启的语义就是"丢弃当前任务并重新加入集群"，
// 因此对已分配槽位的 Agent，注册成功后任务侧仅记录 offline 事件不主动恢复任务，
// 任务调度逻辑会通过心跳超时安全网正常处理。
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
		ID:              req.AgentID,
		Name:            req.Name,
		Address:         req.Address,
		AppVersion:      req.AppVersion,
		MaxBots:         req.MaxBots,
		StressInterval:  req.StressInterval,
		SystemInterval:  req.SystemInterval,
		StaticInfo:      req.StaticInfo,
		Status:          AgentIdle,
		LastHeartbeatAt: time.Now(),
	}
	if err := s.agents.Register(node); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, RegisterResponse{
		AgentID:        req.AgentID,
		HeartbeatTTL:   s.cfg.AgentRegistry.UnhealthyAfter,
		StressEndpoint: "/sbot/agent/stress",
		SystemEndpoint: "/sbot/agent/system",
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
//
// 用户需求 §6.1：任意 Agent 请求都视为 keepalive，刷新 LastHeartbeatAt。
func (s *AdminServer) handleAgentStressReport(w http.ResponseWriter, r *http.Request) {
	var report StressReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeError(w, ErrInvalidArgument)
		return
	}
	s.agents.Touch(report.AgentID, "")
	var currentTaskID string
	if agent, ok := s.agents.Get(report.AgentID); ok {
		currentTaskID = agent.CurrentTaskID
	}
	if currentTaskID == "" || (report.TaskID != "" && report.TaskID != currentTaskID) {
		// 旧任务的延迟报告 / agent 已 idle，直接丢弃，避免 LatestStress 被串。
		stresslog.Debug("丢弃过期 stress 报告",
			zap.String("agentId", report.AgentID),
			zap.String("reportTaskId", report.TaskID),
			zap.String("currentTaskId", currentTaskID))
		writeJSON(w, http.StatusOK, map[string]string{"status": "stale"})
		return
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
	s.agents.Touch(report.AgentID, "")
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

	// 用户需求 §6.1：任意 Agent 请求都视为 keepalive。
	// 阶段报告（StageIndex > 0）只刷新心跳时间，**不能**清空 CurrentTaskID/LatestStress：
	// 任务仍在运行，清空会导致：
	//   1) 聚合器 AggregateStress 因 currentTaskID 不匹配而排除该节点
	//   2) 后续 stress 报告被 handleAgentStressReport 当成 stale 丢弃
	//   3) checkAndStopIfAllLost 把节点视为"已失效"误触发自动停止
	//   4) 若节点恰好处于 unhealthy，touchLocked 会因 CurrentTaskID="" 把 Status 回到 idle，
	//      进而触发 onAgentStatusChange 的"restarted"合成失败报告，整个任务被错误地标记为完成
	s.agents.Touch(agentID, "")
	isFinal := report.StageIndex <= 0
	if isFinal {
		// 仅最终报告才把节点 marked back to idle；这两个操作走 Touch + Heartbeat 路径，
		// 必须在 tasks.Update 之外完成，避免 agents.mu 与 tasks.mu 的 AB-BA 死锁。
		if err := s.agents.Heartbeat(agentID, HeartbeatRequest{
			AgentID: agentID,
			Status:  "idle",
		}); err != nil {
			// Agent 已被 admin 删除等场景，不影响 report 入库
			stresslog.Warn("[ADMIN] handleAgentTaskDone Heartbeat 失败",
				zap.String("agentId", agentID), zap.Error(err))
		}
	}

	var needTransition TaskState // 零值表示不需要转换
	err := s.tasks.Update(taskID, func(t *Task) {
		// 阶段完成报告（渐进式加压 reset 阶段）：存入 StageReports，不触发状态转换。
		// 幂等性：同一 (agentId, stageIndex) 已存在则覆盖（重试场景），不重复 append。
		if !isFinal {
			replaced := false
			for i := range t.StageReports {
				if t.StageReports[i].AgentID == agentID && t.StageReports[i].StageIndex == report.StageIndex {
					t.StageReports[i] = report
					replaced = true
					break
				}
			}
			if !replaced {
				t.StageReports = append(t.StageReports, report)
			}
			stresslog.Info("[ADMIN] 收到阶段完成报告",
				zap.String("taskId", taskID),
				zap.String("agentId", agentID),
				zap.Int("stageIndex", report.StageIndex),
				zap.Bool("dedup", replaced))
			return
		}

		// 最终完成报告
		if t.Reports == nil {
			t.Reports = make(map[string]TaskCompletionReport)
		}
		t.Reports[agentID] = report

		// 检查是否全部完成：只等实际成功的 Agent
		expected := len(t.SucceededAgents)
		if expected == 0 {
			expected = len(t.Assignments)
		}
		if len(t.Reports) == expected {
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

	// Transition 必须在 Update 外部调用，避免死锁（两者都拿 ts.mu）。
	// 错误必须检查：状态转换非法属于内部一致性问题，需要日志记录。
	if needTransition == TaskRunning {
		if _, err := s.tasks.Transition(taskID, TaskRunning, TaskStopped); err != nil {
			stresslog.Warn("[ADMIN] 状态转换失败 running→stopped",
				zap.String("taskId", taskID), zap.Error(err))
		}
	} else if needTransition == TaskStopping {
		if _, err := s.tasks.Transition(taskID, TaskStopping, TaskStopped); err != nil {
			stresslog.Warn("[ADMIN] 状态转换失败 stopping→stopped",
				zap.String("taskId", taskID), zap.Error(err))
		}
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
	currentTaskID := node.CurrentTaskID
	s.agents.Touch(agentID, "")
	if currentTaskID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"taskId": currentTaskID,
	})
}

// ── 前端-任务 ──

// handleCreateTask multipart/form-data 创建任务。
func (s *AdminServer) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	const maxMultipartMB = 32
	if err := r.ParseMultipartForm(int64(maxMultipartMB) << 20); err != nil {
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
	flowData, err := io.ReadAll(flowFile)
	_ = flowFile.Close() // multipart 文件句柄，ReadAll 后关闭即可
	if err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("failed to read flow.json"))
		return
	}
	cfg.FlowJSON = json.RawMessage(flowData)

	// proto 文件
	cfg.ProtoFiles = make(map[string][]byte)
	for key, files := range r.MultipartForm.File {
		if strings.HasPrefix(key, "proto/") || key == "proto" {
			for _, fh := range files {
				f, err := fh.Open()
				if err != nil {
					continue
				}
				data, err := io.ReadAll(f)
				_ = f.Close() // multipart 文件句柄，ReadAll 后关闭即可
				if err != nil {
					stresslog.Warn("[ADMIN] 读取 proto 文件失败", zap.String("name", fh.Filename), zap.Error(err))
					continue
				}
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
				data, err := io.ReadAll(f)
				_ = f.Close() // multipart 文件句柄，ReadAll 后关闭即可
				if err != nil {
					stresslog.Warn("[ADMIN] 读取脚本文件失败", zap.String("name", fh.Filename), zap.Error(err))
					continue
				}
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

	// 协议适配器（adapter/codec.lua，可选）
	if adapterFile, _, err := r.FormFile("adapter/codec.lua"); err == nil {
		adapterData, err := io.ReadAll(adapterFile)
		if err != nil {
			stresslog.Warn("[ADMIN] 读取适配器脚本失败", zap.Error(err))
		}
		_ = adapterFile.Close() // multipart 文件句柄，ReadAll 后关闭即可
		cfg.AdapterScript = adapterData
	}

	// 可选：error.lua
	if errorMapFile, _, err := r.FormFile("adapter/error.lua"); err == nil {
		errorMapData, err := io.ReadAll(errorMapFile)
		if err != nil {
			stresslog.Warn("[ADMIN] 读取 error.lua 失败", zap.Error(err))
		}
		_ = errorMapFile.Close() // multipart 文件句柄，ReadAll 后关闭即可
		cfg.ErrorMapScript = errorMapData
	}

	// robotConfig（JSON string）
	if rc := r.FormValue("robotConfig"); rc != "" {
		if err := json.Unmarshal([]byte(rc), &cfg.RobotConfig); err != nil {
			writeError(w, ErrInvalidArgument.WithMessage("invalid robotConfig JSON"))
			return
		}
	}

	// 校验 rampUp 配置
	if cfg.RobotConfig.RampUp != nil {
		if len(cfg.RobotConfig.RampUp.Stages) == 0 {
			writeError(w, ErrInvalidArgument.WithMessage("rampUp.stages must not be empty"))
			return
		}
		sum := 0
		for _, s := range cfg.RobotConfig.RampUp.Stages {
			if s.Count <= 0 {
				writeError(w, ErrInvalidArgument.WithMessage("rampUp.stage.count must be positive"))
				return
			}
			if s.HoldSec < 0 {
				writeError(w, ErrInvalidArgument.WithMessage("rampUp.stage.holdSec must be >= 0"))
				return
			}
			sum += s.Count
		}
		if sum != totalBots {
			writeError(w, ErrInvalidArgument.WithMessage(fmt.Sprintf("rampUp stages count sum (%d) must equal totalBots (%d)", sum, totalBots)))
			return
		}
	}

	// deadline（可选）
	if dl := r.FormValue("deadline"); dl != "" {
		if t, err := time.Parse(time.RFC3339, dl); err == nil {
			cfg.Deadline = &t
		}
	}

	// 将上传的资源写入磁盘基线，使前端下次同步时 IDB 与基线一致
	s.writeBaselineFiles(&cfg, flowData)

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
			"agentCount":      len(t.Assignments),
			"activeAgentCount": len(t.SucceededAgents),
			"createdAt":       t.CreatedAt,
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
	case "flow/flow.json":
		if task.Config.FlowJSON == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(task.Config.FlowJSON)

	case "config.json":
		// Agent 运行时配置（robotConfig + 超时等）
		// 内联 JSON，序列化已知类型不会失败
		configJSON, _ := json.Marshal(map[string]any{
			"concurrency": task.Config.RobotConfig.Concurrency,
			"timeoutSec":  task.Config.RobotConfig.TimeoutSec,
			"deadline":    task.Config.Deadline,
		})
		w.Header().Set("Content-Type", "application/json")
		w.Write(configJSON)

	case "adapter/codec.lua":
		if task.Config.AdapterScript == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write(task.Config.AdapterScript)

	case "adapter/error.lua":
		if task.Config.ErrorMapScript == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write(task.Config.ErrorMapScript)

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
		if _, terr := s.tasks.Transition(id, TaskStarting, TaskFailed); terr != nil {
			stresslog.Warn("[ADMIN] 状态转换失败 starting→failed",
				zap.String("taskId", id), zap.Error(terr))
		}
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
	// 读取任务配置填充 TaskAssignment
	task, ok := s.tasks.Get(taskID)
	if !ok || task == nil {
		// 任务在异步路径里已被删除 / 不存在：把任务标记为 failed 并清理 sampler
		stresslog.Error("[ADMIN] 任务不存在，取消下发", zap.String("taskId", taskID))
		if _, err := s.tasks.Transition(taskID, TaskStarting, TaskFailed); err != nil {
			stresslog.Warn("[ADMIN] 状态转换失败 starting→failed",
				zap.String("taskId", taskID), zap.Error(err))
		}
		if s.sampler != nil {
			s.sampler.Stop(taskID)
		}
		return
	}
	rc := task.Config.RobotConfig
	taskTotalBots := task.TotalBots

	// 构建配置文件清单
	var configFiles []string
	if task.Config.FlowJSON != nil {
		configFiles = append(configFiles, "flow/flow.json")
	}
	for name := range task.Config.ProtoFiles {
		configFiles = append(configFiles, "proto/"+name)
	}
	for name := range task.Config.LuaScripts {
		configFiles = append(configFiles, "scripts/"+name)
	}
	if task.Config.AdapterScript != nil {
		configFiles = append(configFiles, "adapter/codec.lua")
	}
	if task.Config.ErrorMapScript != nil {
		configFiles = append(configFiles, "adapter/error.lua")
	}

	// 并行下发到所有 Agent：避免单一慢节点（30s 超时 * 3 次重试 ≈ 90s）阻塞其他节点的启动，
	// 也避免"只有第一个 Agent 收到任务"的视感。每个节点独立成功/失败收敛后再做状态转换。
	type pushResult struct {
		agentID string
		err     error
		bots    int
	}
	resultCh := make(chan pushResult, len(assignments))
	for _, a := range assignments {
		a := a // 捕获循环变量
		agent, ok := s.agents.Get(a.AgentID)
		if !ok {
			resultCh <- pushResult{agentID: a.AgentID, err: fmt.Errorf("agent not found")}
			continue
		}
		cfg := TaskAssignment{
			TaskID:            taskID,
			TaskName:          taskName,
			StartNumber:       a.StartNumber,
			TotalBots:         a.TotalBots,
			AccountPrefix:     stringOr(rc.AccountPrefix, "bot_", "robotConfig.accountPrefix"),
			MainService:       rc.MainService,
			StateExtra:        rc.StateExtra,
			ConcurrentNum:     rc.Concurrency,
			HeartbeatInterval: secsOr(rc.HeartbeatSec, 5, "robotConfig.heartbeatSec"),
			TCPTimeout:        secsOr(rc.TimeoutSec, 60, "robotConfig.timeoutSec"),
			HTTPTimeout:       secsOr(rc.HTTPTimeoutSec, 10, "robotConfig.httpTimeoutSec"),
			ApdexT:            intOr(rc.ApdexT, 100, "robotConfig.apdexT"),
			LogLevel:          rc.LogLevel,
			ConfigURL:         fmt.Sprintf("%s/sbot/tasks/%s/config", s.cfg.PublicURL, taskID),
			ConfigFiles:       configFiles,
			RampUp:            scaleRampUp(rc.RampUp, taskTotalBots, a.TotalBots),
		}
		addr := agent.Address
		agentID := a.AgentID
		bots := a.TotalBots
		utils.GetWorkPool().Go(func() {
			err := s.dispatcher.AssignTask(addr, cfg)
			resultCh <- pushResult{agentID: agentID, err: err, bots: bots}
		})
	}

	var failed []string
	var succeeded []string
	for i := 0; i < len(assignments); i++ {
		r := <-resultCh
		if r.err != nil {
			failed = append(failed, r.agentID)
			stresslog.Error("推送任务失败",
				zap.String("agentId", r.agentID),
				zap.Error(r.err))
			continue
		}
		succeeded = append(succeeded, r.agentID)
		s.agents.Heartbeat(r.agentID, HeartbeatRequest{
			AgentID:       r.agentID,
			Status:        "busy",
			CurrentTaskID: taskID,
			CurrentBots:   r.bots,
		})
	}

	if len(failed) > 0 {
		stresslog.Warn("部分 Agent 推送任务失败",
			zap.String("taskId", taskID),
			zap.Strings("failedAgents", failed),
			zap.Strings("succeededAgents", succeeded))

		if len(succeeded) == 0 {
			// 全部失败 → 标记任务 failed
			if _, err := s.tasks.Transition(taskID, TaskStarting, TaskFailed); err != nil {
				stresslog.Warn("[ADMIN] 状态转换失败 starting→failed",
					zap.String("taskId", taskID), zap.Error(err))
			}
			if s.sampler != nil {
				s.sampler.Stop(taskID)
			}
			stresslog.Error("任务启动失败，无 Agent 成功",
				zap.String("taskId", taskID),
				zap.Strings("failedAgents", failed))
			return
		}
		// 部分成功 → 继续执行
		stresslog.Warn("任务将以部分 Agent 继续执行",
			zap.String("taskId", taskID),
			zap.Int("expectedAgents", len(assignments)),
			zap.Int("actualAgents", len(succeeded)))
	}

	// 记录实际成功的 Agent 列表，用于完成判定
	s.tasks.Update(taskID, func(t *Task) {
		t.SucceededAgents = succeeded
	})

	if _, err := s.tasks.Transition(taskID, TaskStarting, TaskRunning); err != nil {
		stresslog.Warn("[ADMIN] 状态转换失败 starting→running",
			zap.String("taskId", taskID), zap.Error(err))
	}
	stresslog.Info("任务启动成功",
		zap.String("taskId", taskID),
		zap.Int("agents", len(succeeded)))
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

	// 向实际运行任务的节点发送 stop。
	// 并行下发：单个 Agent stop RPC 可能因为节点 IO 阻塞而耗时（HTTP 客户端超时 ~30s），
	// 串行会让"第二个节点"额外等待第一个节点的超时，给用户造成"只对一个 Agent 生效"的错觉。
	targets := task.SucceededAgents
	if len(targets) == 0 {
		targets = make([]string, 0, len(task.Assignments))
		for _, a := range task.Assignments {
			targets = append(targets, a.AgentID)
		}
	}

	var wg sync.WaitGroup
	for _, agentID := range targets {
		agentID := agentID
		agent, ok := s.agents.Get(agentID)
		if !ok {
			stresslog.Warn("停止跳过：节点未找到",
				zap.String("agentId", agentID))
			continue
		}
		if agent.Status == AgentOffline {
			stresslog.Warn("停止跳过：节点离线",
				zap.String("agentId", agentID),
				zap.String("address", agent.Address))
			continue
		}
		addr := agent.Address
		wg.Add(1)
		utils.GetWorkPool().Go(func() {
			defer wg.Done()
			if err := s.dispatcher.Stop(addr, id); err != nil {
				stresslog.Warn("停止命令发送失败",
					zap.String("agentId", agentID),
					zap.String("address", addr),
					zap.Error(err))
			} else {
				stresslog.Info("停止命令已发送",
					zap.String("agentId", agentID),
					zap.String("address", addr))
			}
		})
	}
	wg.Wait()

	// 立刻为已离线且未上报的节点合成 stopped report（Admin 已知它们不可能再上报了）
	allReported := s.synthesizeOfflineReports(id)

	// 如果全部节点都已有 report（例如所有节点早已离线），直接转 stopped
	if allReported {
		if _, err := s.tasks.Transition(id, TaskStopping, TaskStopped); err != nil {
			stresslog.Warn("[ADMIN] 状态转换失败 stopping→stopped",
				zap.String("taskId", id), zap.Error(err))
		}
	} else {
		// 安全网：30s 后如果还在 stopping（在线节点未响应），强制完成
		s.startStopTimeout(id)
	}

	updated, _ := s.tasks.Get(id) // 同上
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":               id,
		"name":             updated.Name,
		"state":            updated.State,
		"totalBots":        updated.TotalBots,
		"agentCount":       len(updated.Assignments),
		"activeAgentCount": len(updated.SucceededAgents),
		"createdAt":        updated.CreatedAt,
	})
}

func (s *AdminServer) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.Delete(id); err != nil {
		writeError(w, err)
		return
	}
stresslog.Info("[ADMIN] 任务已删除", zap.String("taskID", id))
	w.WriteHeader(http.StatusNoContent)
}

// ── 前端-Agent ──

func (s *AdminServer) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.agents.List()

	items := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		brief := map[string]any{
			"agentId":         a.ID,
			"name":            a.Name,
			"address":         a.Address,
			"appVersion":      a.AppVersion,
			"maxBots":         a.MaxBots,
			"status":          a.Status,
			"currentTaskId":   a.CurrentTaskID,
			"currentBots":     a.CurrentBots,
			"staticInfo":      a.StaticInfo,
			"lastHeartbeatAt": a.LastHeartbeatAt,
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
stresslog.Info("[ADMIN] 节点已删除", zap.String("agentID", id), zap.String("agentName", agent.Name))
	w.WriteHeader(http.StatusNoContent)
}

func (s *AdminServer) handleShutdownAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, ok := s.agents.Get(id)
	if !ok {
		writeError(w, ErrAgentNotFound)
		return
	}
	if agent.Status == AgentOffline {
		writeError(w, ErrAgentOffline.WithMessage("agent is offline, cannot send shutdown"))
		return
	}
	if err := s.dispatcher.Shutdown(agent.Address); err != nil {
		stresslog.Warn("关闭命令发送失败", zap.String("agentId", id), zap.Error(err))
		writeError(w, ErrAgentOffline.WithMessage("agent unreachable: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "shutdown_sent"})
}

func (s *AdminServer) handleShutdownAllAgents(w http.ResponseWriter, _ *http.Request) {
	all := s.agents.List()
	var succeeded, failed []string
	for _, a := range all {
		if a.Status == AgentOffline {
			continue
		}
		if err := s.dispatcher.Shutdown(a.Address); err != nil {
			failed = append(failed, a.ID)
			stresslog.Warn("关闭命令发送失败", zap.String("agentId", a.ID), zap.Error(err))
		} else {
			succeeded = append(succeeded, a.ID)
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "shutdown_sent",
		"succeeded": succeeded,
		"failed":    failed,
	})
}

// ── 前端-指标 ──

func (s *AdminServer) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	active := s.tasks.ActiveTask()
	if active == nil {
		writeJSON(w, http.StatusOK, &monitor.CollectorSnapshot{})
		return
	}
	agg := s.aggregator.AggregateStress(active.ID)
	writeJSON(w, http.StatusOK, agg)
}

// handleGetMetricsSummary 文本摘要。
func (s *AdminServer) handleGetMetricsSummary(w http.ResponseWriter, r *http.Request) {
	active := s.tasks.ActiveTask()
	if active == nil {
		writeJSON(w, http.StatusOK, map[string]string{"summary": "no active task"})
		return
	}

	agg := s.aggregator.AggregateStress(active.ID)
	var b strings.Builder
	fmt.Fprintf(&b, "Task: %s (%s)\n", active.Name, active.ID)
	fmt.Fprintf(&b, "Total Actions: %d\n", agg.Snapshot.TotalActions)
	if len(agg.Snapshot.Actions) > 0 {
		for _, a := range agg.Snapshot.Actions {
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

// ── 日志 ──

func (s *AdminServer) handleGetAdminLogs(w http.ResponseWriter, r *http.Request) {
	rb := logview.GetRingBuffer()
	if rb == nil {
		writeJSON(w, http.StatusOK, logview.QueryResult{})
		return
	}
	params := parseLogQueryParams(r)
	result := rb.Query(params)
	writeJSON(w, http.StatusOK, result)
}

func (s *AdminServer) handleGetAgentLogs(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	agent, ok := s.agents.Get(agentID)
	if !ok {
		writeError(w, ErrAgentNotFound)
		return
	}
	if agent.Status == AgentOffline {
		writeError(w, ErrAgentOffline.WithMessage("agent is offline, logs unavailable"))
		return
	}

	url := fmt.Sprintf("http://%s/agent/v1/logs?%s", normalizeAddr(agent.Address), r.URL.RawQuery)
	proxyReq, err := http.NewRequestWithContext(r.Context(), "GET", url, nil)
	if err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("invalid proxy request"))
		return
	}

	resp, err := s.logsProxyClient.Do(proxyReq)
	if err != nil {
		writeError(w, ErrAgentOffline.WithMessage("agent unreachable: "+err.Error()))
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		io.Copy(io.Discard, resp.Body) // 确保响应体被完全消耗，允许连接复用
	}
}

// LogFileInfo 日志文件信息。
type LogFileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

func (s *AdminServer) handleListAdminLogFiles(w http.ResponseWriter, r *http.Request) {
	files, err := listLogFiles(stresslog.GetLogFilePath())
	if err != nil {
		writeError(w, ErrInternal.WithMessage(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *AdminServer) handleDownloadAdminLogFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || name == ".." {
		writeError(w, ErrInvalidArgument.WithMessage("invalid file name"))
		return
	}
	dir := filepath.Dir(stresslog.GetLogFilePath())
	serveLogFile(w, r, dir, name)
}

func (s *AdminServer) handleListAgentLogFiles(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	agent, ok := s.agents.Get(agentID)
	if !ok {
		writeError(w, ErrAgentNotFound)
		return
	}
	if agent.Status == AgentOffline {
		writeError(w, ErrAgentOffline)
		return
	}

	url := fmt.Sprintf("http://%s/agent/v1/logs/files", normalizeAddr(agent.Address))
	proxyReq, err := http.NewRequestWithContext(r.Context(), "GET", url, nil)
	if err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("invalid proxy request"))
		return
	}

	resp, err := s.logsProxyClient.Do(proxyReq)
	if err != nil {
		writeError(w, ErrAgentOffline.WithMessage("agent unreachable: "+err.Error()))
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		io.Copy(io.Discard, resp.Body) // 确保响应体被完全消耗，允许连接复用
	}
}

func (s *AdminServer) handleDownloadAgentLogFile(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	agent, ok := s.agents.Get(agentID)
	if !ok {
		writeError(w, ErrAgentNotFound)
		return
	}
	if agent.Status == AgentOffline {
		writeError(w, ErrAgentOffline)
		return
	}

	name := r.PathValue("name")
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || name == ".." {
		writeError(w, ErrInvalidArgument.WithMessage("invalid file name"))
		return
	}

	url := fmt.Sprintf("http://%s/agent/v1/logs/files/%s", normalizeAddr(agent.Address), url.PathEscape(name))
	proxyReq, err := http.NewRequestWithContext(r.Context(), "GET", url, nil)
	if err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("invalid proxy request"))
		return
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		writeError(w, ErrAgentOffline.WithMessage("agent unreachable: "+err.Error()))
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		io.Copy(io.Discard, resp.Body) // 确保响应体被完全消耗，允许连接复用
	}
}

func listLogFiles(logPath string) ([]LogFileInfo, error) {
	dir := filepath.Dir(logPath)
	base := filepath.Base(logPath)
	prefix := strings.TrimSuffix(base, filepath.Ext(base))

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read log dir: %w", err)
	}

	var files []LogFileInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, LogFileInfo{
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	return files, nil
}

func serveLogFile(w http.ResponseWriter, r *http.Request, dir, name string) {
	path := filepath.Join(dir, name)
	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "log file not found"})
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stat failed"})
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	http.ServeContent(w, r, name, stat.ModTime(), f)
}

// handleUpdateBaseline 前端主动推送 IDB 资源到磁盘基线。
// 接受 multipart/form-data（proto/scripts/adapter），写入 conf/ 目录。
func (s *AdminServer) handleUpdateBaseline(w http.ResponseWriter, r *http.Request) {
	const maxMultipartMB = 32
	if err := r.ParseMultipartForm(int64(maxMultipartMB) << 20); err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("multipart parse error"))
		return
	}

	// proto 文件
	for key, files := range r.MultipartForm.File {
		if !strings.HasPrefix(key, "proto/") && key != "proto" {
			continue
		}
		for _, fh := range files {
			f, err := fh.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(f)
			_ = f.Close() // multipart 文件句柄，ReadAll 后关闭即可
			if err != nil {
				stresslog.Warn("[ADMIN] 读取基线文件失败", zap.String("name", fh.Filename), zap.Error(err))
				continue
			}
			var fileName string
			if strings.HasPrefix(key, "proto/") && key != "proto/" {
				fileName = strings.TrimPrefix(key, "proto/")
			} else {
				fileName = fh.Filename
			}
			if err := safeWriteFile("conf/proto", fileName, data); err != nil {
				stresslog.Warn("基线更新 proto 失败",
					zap.String("name", fileName),
					zap.Error(err))
			}
		}
	}

	// lua 脚本
	for key, files := range r.MultipartForm.File {
		if !strings.HasPrefix(key, "scripts/") && key != "scripts" {
			continue
		}
		for _, fh := range files {
			f, err := fh.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(f)
			_ = f.Close() // multipart 文件句柄，ReadAll 后关闭即可
			if err != nil {
				stresslog.Warn("[ADMIN] 读取基线文件失败", zap.String("name", fh.Filename), zap.Error(err))
				continue
			}
			var fileName string
			if strings.HasPrefix(key, "scripts/") && key != "scripts/" {
				fileName = strings.TrimPrefix(key, "scripts/")
			} else {
				fileName = fh.Filename
			}
			if err := safeWriteFile("conf/scripts", fileName, data); err != nil {
				stresslog.Warn("基线更新脚本失败",
					zap.String("name", fileName),
					zap.Error(err))
			}
		}
	}

	// adapter
	if adapterFile, _, err := r.FormFile("adapter/codec.lua"); err == nil {
		adapterData, err := io.ReadAll(adapterFile)
		_ = adapterFile.Close() // multipart 文件句柄，ReadAll 后关闭即可
		if err != nil {
			stresslog.Warn("[ADMIN] 读取基线适配器文件失败", zap.Error(err))
		} else if err := safeWriteFile("conf/adapter", "codec.lua", adapterData); err != nil {
			stresslog.Warn("基线更新适配器失败", zap.Error(err))
		}
	}

	// 可选：error.lua
	if errorMapFile, _, err := r.FormFile("adapter/error.lua"); err == nil {
		errorMapData, err := io.ReadAll(errorMapFile)
		_ = errorMapFile.Close() // multipart 文件句柄，ReadAll 后关闭即可
		if err != nil {
			stresslog.Warn("[ADMIN] 读取基线错误映射文件失败", zap.Error(err))
		} else if err := safeWriteFile("conf/adapter", "error.lua", errorMapData); err != nil {
			stresslog.Warn("基线更新错误映射失败", zap.Error(err))
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeBaselineFiles 将上传的 flow/proto/scripts/adapter 写入磁盘基线目录，
// 使前端下次同步时 IDB 与基线一致，不再误报冲突。
func (s *AdminServer) writeBaselineFiles(cfg *TaskConfig, flowData []byte) {
	if err := safeWriteFile("conf/flow", "flow.json", flowData); err != nil {
		stresslog.Warn("写入基线 flow.json 失败", zap.Error(err))
	}
	for name, data := range cfg.ProtoFiles {
		if err := safeWriteFile("conf/proto", name, data); err != nil {
			stresslog.Warn("写入基线 proto 失败",
				zap.String("name", name),
				zap.Error(err))
		}
	}
	for name, data := range cfg.LuaScripts {
		if err := safeWriteFile("conf/scripts", name, data); err != nil {
			stresslog.Warn("写入基线脚本失败",
				zap.String("name", name),
				zap.Error(err))
		}
	}
	if cfg.AdapterScript != nil {
		if err := safeWriteFile("conf/adapter", "codec.lua", cfg.AdapterScript); err != nil {
			stresslog.Warn("写入基线适配器失败",
				zap.Error(err))
		}
	}
	if cfg.ErrorMapScript != nil {
		if err := safeWriteFile("conf/adapter", "error.lua", cfg.ErrorMapScript); err != nil {
			stresslog.Warn("写入基线错误映射失败",
				zap.Error(err))
		}
	}
}

// safeWriteFile 将 data 写入 dir/name，自动创建目录，防止路径穿越。
func safeWriteFile(dir, name string, data []byte) error {
	name = filepath.Base(name)
	if name == "." || name == ".." {
		return fmt.Errorf("invalid file name: %s", name)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, name), data, 0644)
}

// ── 基线资源读取 ──

func (s *AdminServer) handleBaselineProtoIndex(w http.ResponseWriter, r *http.Request) {
	files, err := listDirFiles("conf/proto", ".proto")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *AdminServer) handleBaselineProtoFile(w http.ResponseWriter, r *http.Request) {
	serveBaselineFile(w, r, "conf/proto", "name")
}

func (s *AdminServer) handleBaselineScriptIndex(w http.ResponseWriter, r *http.Request) {
	files, err := listDirFiles("conf/scripts", ".lua")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *AdminServer) handleBaselineScriptFile(w http.ResponseWriter, r *http.Request) {
	serveBaselineFile(w, r, "conf/scripts", "name")
}

func (s *AdminServer) handleBaselineAdapter(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "conf/adapter/codec.lua")
}

func (s *AdminServer) handleBaselineErrorMap(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "conf/adapter/error.lua")
}

func (s *AdminServer) handleErrorCodeIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(errcode.AllCodes()) // 写入 HTTP 响应，错误由 recoverMiddleware 兜底
}

func (s *AdminServer) handleBaselineFlow(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "conf/flow/flow.json")
}

func (s *AdminServer) handleBaselineConfig(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "conf/config.json")
}

// listDirFiles 列出 dir 中后缀为 ext 的文件名（不含路径）。
func listDirFiles(dir, ext string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取目录 %s 失败: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ext) {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// serveBaselineFile 从 dir 目录提供指定文件，用 pathValue(key) 取文件名。
func serveBaselineFile(w http.ResponseWriter, r *http.Request, dir, key string) {
	name := r.PathValue(key)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing file name"})
		return
	}
	// 防止路径穿越
	name = filepath.Base(name)
	if name == "." || name == ".." {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid file name"})
		return
	}
	http.ServeFile(w, r, filepath.Join(dir, name))
}

// scaleRampUp 按比例缩放各 stage 的 count（分布式模式下每个 Agent 分到的 bot 数不同）。
//
// 关键约束：
//  1. 缩放后各 stage.Count 之和**严格等于** assignedBots，不能多也不能少；
//  2. 阶段 count 允许为 0（该 Agent 在此阶段不新增机器人），但严禁负数；
//  3. Reset / HoldSec / Concurrency 等"语义字段"原样保留，缩放只动 Count。
//
// 假设 totalBots=200，两 Agent 各 100（ratio=0.5），stages=[100,150,150]：
//
//	old 实现：第二阶段 round(150*0.5)=75，remaining 走到最后 stage 时
//	         可能因 c<1 强制 1 而出现 remaining 负数 → 末阶段 count 负数。
//
// 新实现：先按 floor 分配（不补 1），再把 remainder 按"最大小数余量"分配，
//
//	保证 Sum=assignedBots 且每个 c ≥ 0。
func scaleRampUp(cfg *RampUpConfig, totalBots, assignedBots int) *RampUpConfig {
	if cfg == nil {
		return nil
	}
	if totalBots <= 0 || assignedBots == totalBots {
		// 单 Agent 全量场景：原样下发；assignedBots 与 totalBots 相等也直接返回原配置
		return cfg
	}
	n := len(cfg.Stages)
	if n == 0 {
		return cfg
	}
	// assignedBots <= 0：该 Agent 实际未分到 bot，缩放为全 0 阶段
	if assignedBots <= 0 {
		scaled := &RampUpConfig{Stages: make([]RampUpStage, 0, n)}
		for _, s := range cfg.Stages {
			scaled.Stages = append(scaled.Stages, RampUpStage{
				Count:       0,
				Concurrency: s.Concurrency,
				Reset:       s.Reset,
				HoldSec:     s.HoldSec,
			})
		}
		return scaled
	}

	counts := make([]int, n)
	fracs := make([]float64, n)
	used := 0
	for i, s := range cfg.Stages {
		exact := float64(s.Count) * float64(assignedBots) / float64(totalBots)
		floor := int(math.Floor(exact))
		if floor < 0 {
			floor = 0
		}
		counts[i] = floor
		fracs[i] = exact - float64(floor)
		used += floor
	}
	// 把剩余的 bot 按余量从大到小补到各 stage，确保总和等于 assignedBots
	remainder := assignedBots - used
	for k := 0; k < remainder; k++ {
		bestIdx := -1
		bestFrac := -1.0
		for i := 0; i < n; i++ {
			if fracs[i] > bestFrac {
				bestFrac = fracs[i]
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			break
		}
		counts[bestIdx]++
		fracs[bestIdx] = -1 // 此 stage 余量已用完
	}
	scaled := &RampUpConfig{Stages: make([]RampUpStage, 0, n)}
	for i, s := range cfg.Stages {
		scaled.Stages = append(scaled.Stages, RampUpStage{
			Count:       counts[i],
			Concurrency: s.Concurrency,
			Reset:       s.Reset,
			HoldSec:     s.HoldSec,
		})
	}
	return scaled
}
