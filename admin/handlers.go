package admin

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"stressbot/errcode"
	"stressbot/monitor"
	configschema "stressbot/schema"
	"stressbot/utils"
	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// registerManagementRoutes 注册浏览器管理 API 与静态资源路由。
//
// 顶层用 recoverMiddleware 包裹，确保 handler panic 不会断开连接，
// 而是返回标准 500 JSON 并把 stack trace 写入应用日志。
func (s *AdminServer) registerManagementRoutes() http.Handler {
	mux := http.NewServeMux()

	// ── 前端-资源基线 ──
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
	mux.HandleFunc("GET /sbot/history/{id}/config/archive", s.handleGetHistoryConfigArchive)
	mux.HandleFunc("GET /sbot/history/{id}/timeseries", s.handleGetHistoryTimeseries)
	mux.HandleFunc("POST /sbot/history/{id}/clone", s.handleCloneHistory)
	mux.HandleFunc("GET /sbot/history/compare", s.handleCompareHistory)

	// ── 基线资源读取 ──
	mux.HandleFunc("GET /sbot/baseline/proto/index.json", s.handleBaselineProtoIndex)
	mux.HandleFunc("GET /sbot/baseline/proto/{name}", s.handleBaselineProtoFile)
	mux.HandleFunc("GET /sbot/baseline/scripts/index.json", s.handleBaselineScriptIndex)
	mux.HandleFunc("GET /sbot/baseline/scripts/{name}", s.handleBaselineScriptFile)
	// adapter 基线按文件名透传（支持多 *_codec.json + errors.json）。
	mux.HandleFunc("GET /sbot/baseline/adapter/index.json", s.handleBaselineCodecIndex)
	mux.HandleFunc("GET /sbot/baseline/adapter/{name}", s.handleBaselineCodecFile)
	mux.HandleFunc("GET /sbot/baseline/flow/flow.json", s.handleBaselineFlow)
	mux.HandleFunc("GET /sbot/baseline/config.json", s.handleBaselineConfig)

	// ── 错误码 ──
	mux.HandleFunc("GET /sbot/api/error-codes", s.handleErrorCodeIndex)

	// ── 流程模板库 ──
	mux.HandleFunc("GET /sbot/flows", s.handleListFlows)
	mux.HandleFunc("POST /sbot/flows", s.handleCreateFlow)
	mux.HandleFunc("GET /sbot/flows/snapshot", s.handleGetFlowSnapshot)
	mux.HandleFunc("PUT /sbot/flows/snapshot", s.handleReplaceFlowSnapshot)
	mux.HandleFunc("GET /sbot/flows/{id}", s.handleGetFlow)
	mux.HandleFunc("PUT /sbot/flows/{id}", s.handleUpdateFlow)
	mux.HandleFunc("DELETE /sbot/flows/{id}", s.handleDeleteFlow)

	// ── Action/Listen 模板库 ──
	mux.HandleFunc("GET /sbot/action-templates", s.handleListActionTemplates)
	mux.HandleFunc("POST /sbot/action-templates", s.handleCreateActionTemplate)
	mux.HandleFunc("GET /sbot/action-templates/snapshot", s.handleGetActionTemplateSnapshot)
	mux.HandleFunc("PUT /sbot/action-templates/snapshot", s.handleReplaceActionTemplateSnapshot)
	mux.HandleFunc("GET /sbot/action-templates/{id}", s.handleGetActionTemplate)
	mux.HandleFunc("PUT /sbot/action-templates/{id}", s.handleUpdateActionTemplate)
	mux.HandleFunc("DELETE /sbot/action-templates/{id}", s.handleDeleteActionTemplate)
	mux.HandleFunc("GET /sbot/listen-templates", s.handleListListenTemplates)
	mux.HandleFunc("POST /sbot/listen-templates", s.handleCreateListenTemplate)
	mux.HandleFunc("GET /sbot/listen-templates/snapshot", s.handleGetListenTemplateSnapshot)
	mux.HandleFunc("PUT /sbot/listen-templates/snapshot", s.handleReplaceListenTemplateSnapshot)
	mux.HandleFunc("GET /sbot/listen-templates/{id}", s.handleGetListenTemplate)
	mux.HandleFunc("PUT /sbot/listen-templates/{id}", s.handleUpdateListenTemplate)
	mux.HandleFunc("DELETE /sbot/listen-templates/{id}", s.handleDeleteListenTemplate)

	// ── 服务器能力 ──
	mux.HandleFunc("GET /sbot/capabilities", s.handleCapabilities)

	// ── Codec 预览/算法元数据（T4.2，纯计算，供前端 codec 编辑器调用）──
	mux.HandleFunc("POST /sbot/codec/preview", s.handleCodecPreview)
	mux.HandleFunc("GET /sbot/codec/algorithms", s.handleCodecAlgorithms)

	// ── 静态资源 ──
	fs := http.FileServer(http.Dir(s.cfg.StaticDir))
	mux.Handle("/", fs)

	return recoverMiddleware(managementOpenAPIValidator(mux))
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

// CapabilitiesResponse 服务器能力查询响应。
type CapabilitiesResponse struct {
	// SharedState 是否已配置共享状态（Redis）。前端据此提示脚本是否可用 share。
	SharedState bool `json:"sharedState"`
	// SharedAddr Redis 地址。仅当 SharedState=true 时有值。
	SharedAddr string `json:"sharedAddr,omitempty"`
	// FlowLibrary 是否已启用服务器流程库。
	FlowLibrary bool `json:"flowLibrary"`
	// TemplateLibrary 是否已启用共享 Action/Listen 模板库；两类存储均可用时才为 true。
	TemplateLibrary bool `json:"templateLibrary"`
}

// handleCapabilities 返回服务器能力（当前仅共享状态可用性），供前端展示与校验提示。
// 出于安全考虑，不返回原始 Redis 地址，只返回脱敏后的展示地址。
func (s *AdminServer) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	resp := CapabilitiesResponse{
		SharedState:     s.cfg.RedisEnabled(),
		FlowLibrary:     s.flows != nil,
		TemplateLibrary: s.actionTemplates != nil && s.listenTemplates != nil,
	}
	if resp.SharedState {
		if resolved, err := s.cfg.Redis.Resolve(); err == nil {
			resp.SharedAddr = fmt.Sprintf("%s:%d", resolved.Host, resolved.Port)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// taskExpectedAgents 返回该任务合法完成上报的 Agent 集合：优先 SucceededAgents，
// 为空时回退到 Assignments 的 Agent 列表（与完成阈值口径一致）。
// 用于校验 gRPC 最终报告来源，拒绝未分配节点的伪造或迟到报告。
func taskExpectedAgents(t *Task) map[string]struct{} {
	set := make(map[string]struct{})
	if len(t.SucceededAgents) > 0 {
		for _, id := range t.SucceededAgents {
			set[id] = struct{}{}
		}
		return set
	}
	for _, a := range t.Assignments {
		set[a.AgentID] = struct{}{}
	}
	return set
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
	if err := configschema.ValidateFlow(flowData); err != nil {
		writeError(w, ErrInvalidArgument.WithMessage(err.Error()))
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

	// 声明式 codec（每连接一份 adapter/*_codec.json）+ 共享 adapter/errors.json。
	// 按 form field 名收集：field 形如 adapter/<basename>.json，basename 作为 Codecs key。
	// errors.json 单独落到 cfg.ErrorMap。
	cfg.Codecs = make(map[string][]byte)
	for key, files := range r.MultipartForm.File {
		if !strings.HasPrefix(key, "adapter/") {
			continue
		}
		name := strings.TrimPrefix(key, "adapter/")
		if name == "" {
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
				stresslog.Warn("[ADMIN] 读取 codec 文件失败",
					zap.String("name", fh.Filename), zap.Error(err))
				continue
			}
			if name == "errors.json" {
				cfg.ErrorMap = data
				continue
			}
			if !strings.HasSuffix(name, "_codec.json") {
				stresslog.Warn("[ADMIN] adapter 目录下非 *_codec.json/errors.json 文件已忽略",
					zap.String("name", name))
				continue
			}
			if err := configschema.ValidateCodec(data); err != nil {
				writeError(w, ErrInvalidArgument.WithMessage(fmt.Sprintf("%s: %v", name, err)))
				return
			}
			cfg.Codecs[name] = data
		}
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

	// flowTemplateId（可选）：来源流程模板 ID，仅用于历史溯源。
	// 实际运行 flow 仍来自上面的 flow.json，不反查模板。MySQL 可用时校验模板存在。
	flowTemplateID := r.FormValue("flowTemplateId")
	if flowTemplateID != "" && s.flows != nil {
		if _, err := s.flows.Get(r.Context(), flowTemplateID); err != nil {
			// 区分"模板不存在"（404）与 DB 故障（500），避免把连接错误误报为模板缺失。
			if ae, ok := err.(*Error); ok && ae.Code == ErrFlowTemplateNotFound.Code {
				writeError(w, ErrFlowTemplateNotFound.WithMessage("来源流程模板不存在"))
			} else {
				writeError(w, ErrInternal.WithMessage(err.Error()))
			}
			return
		}
	}

	// 将上传的资源写入磁盘基线，使前端下次同步时 IDB 与基线一致
	s.writeBaselineFiles(&cfg, flowData)

	task := &Task{
		ID:             generateID(),
		Name:           name,
		State:          TaskPending,
		TotalBots:      totalBots,
		Config:         cfg,
		FlowTemplateID: flowTemplateID,
		CreatedAt:      time.Now(),
	}
	if err := s.tasks.Create(task); err != nil {
		writeError(w, err)
		return
	}

	stresslog.Info("任务创建", zap.String("taskID", task.ID), zap.String("name", task.Name))
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
			"id":               t.ID,
			"name":             t.Name,
			"state":            t.State,
			"totalBots":        t.TotalBots,
			"agentCount":       len(t.Assignments),
			"activeAgentCount": len(t.SucceededAgents),
			"createdAt":        t.CreatedAt,
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

	default:
		// adapter/*_codec.json（按 basename 匹配 Codecs）或 adapter/errors.json
		if after, ok0 := strings.CutPrefix(path, "adapter/"); ok0 {
			name := after
			if name == "errors.json" {
				if len(task.Config.ErrorMap) == 0 {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(task.Config.ErrorMap)
				return
			}
			if data, found := task.Config.Codecs[name]; found {
				w.Header().Set("Content-Type", "application/json")
				w.Write(data)
				return
			}
		}
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
		if _, after, ok := strings.Cut(path, "/"); ok {
			baseName = after
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

	stresslog.Info("[ADMIN] 收到任务启动请求",
		zap.String("taskID", id),
		zap.String("remoteAddr", r.RemoteAddr))

	task, err := s.tasks.StartTask(id)
	if err != nil {
		writeError(w, err)
		return
	}

	// 自动检测共享状态：脚本是否使用 require("share")。
	// 若使用但服务器未配置 Redis，直接失败并提示，避免任务跑起来后 share.* 全部报错。
	sharedUsed := taskUsesShare(task)
	if sharedUsed && !s.cfg.RedisEnabled() {
		if _, terr := s.tasks.Transition(id, TaskStarting, TaskFailed); terr != nil {
			stresslog.Warn("[ADMIN] 状态转换失败 starting→failed",
				zap.String("taskID", id), zap.Error(terr))
		}
		writeError(w, ErrSharedUnavailable.WithMessage("任务脚本使用了共享状态(share)，但服务器未配置 Redis，无法启动"))
		return
	}
	s.tasks.Update(id, func(t *Task) {
		t.SharedUsed = sharedUsed
		t.SharedRunID = id
	})

	// 分配 Agent
	idleAgents := s.agents.ListByStatus(AgentIdle)
	assignments, err := s.assigner.Assign(task, idleAgents, task.Config.RobotConfig.StartNumber)
	if err != nil {
		if _, terr := s.tasks.Transition(id, TaskStarting, TaskFailed); terr != nil {
			stresslog.Warn("[ADMIN] 状态转换失败 starting→failed",
				zap.String("taskID", id), zap.Error(terr))
		}
		writeError(w, err)
		return
	}

	stresslog.Info("[ADMIN] 任务分配完成",
		zap.String("taskID", id),
		zap.String("taskName", task.Name),
		zap.Int("totalBots", task.TotalBots),
		zap.Bool("sharedUsed", sharedUsed),
		zap.Int("idleAgents", len(idleAgents)),
		zap.Int("assignments", len(assignments)))

	if err := validateDistributedConcurrency(task.Config.RobotConfig, assignments); err != nil {
		if _, terr := s.tasks.Transition(id, TaskStarting, TaskFailed); terr != nil {
			stresslog.Warn("[ADMIN] 状态转换失败 starting→failed",
				zap.String("taskID", id), zap.Error(terr))
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
	task, ok := s.tasks.Get(taskID)
	if !ok || task == nil {
		stresslog.Error("[ADMIN] 任务不存在，取消下发", zap.String("taskID", taskID))
		if s.sampler != nil {
			s.sampler.Stop(taskID)
		}
		return
	}
	ctx, cancel := commandContext()
	succeeded, err := s.scheduleStartCommands(ctx, task, assignments)
	cancel()
	if err == nil {
		_ = s.tasks.Update(taskID, func(current *Task) { current.SucceededAgents = succeeded })
		_, err = s.tasks.Transition(taskID, TaskStarting, TaskRunning)
		if err == nil {
			s.finishTaskIfFullyReported(taskID)
		}
	}
	if err != nil {
		stresslog.Error("[ADMIN] 创建 gRPC 启动命令失败", zap.String("taskID", taskID), zap.String("taskName", taskName), zap.Error(err))
		_, _ = s.tasks.Transition(taskID, TaskStarting, TaskFailed)
		if s.sampler != nil {
			s.sampler.Stop(taskID)
		}
		return
	}
	stresslog.Info("[ADMIN] gRPC 启动命令已创建并调度", zap.String("taskID", taskID), zap.Int("agents", len(succeeded)))
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

	stresslog.Info("[ADMIN] 收到任务停止请求",
		zap.String("taskID", id),
		zap.String("remoteAddr", r.RemoteAddr),
		zap.String("state", string(task.State)))

	{
		grpcTargets := append([]string(nil), task.SucceededAgents...)
		if len(grpcTargets) == 0 {
			for _, assignment := range task.Assignments {
				grpcTargets = append(grpcTargets, assignment.AgentID)
			}
		}
		ctx, cancel := commandContext()
		err := s.scheduleStopCommands(ctx, id, grpcTargets, "用户停止任务")
		cancel()
		if err != nil {
			_, _ = s.tasks.Transition(id, TaskStopping, TaskRunning)
			writeError(w, ErrInternal.WithMessage("创建停止命令失败: "+err.Error()))
			return
		}
		allReported := s.synthesizeOfflineReports(id)
		if allReported {
			_, _ = s.tasks.Transition(id, TaskStopping, TaskStopped)
		} else {
			s.startStopTimeout(id)
		}
		updated, _ := s.tasks.Get(id)
		writeJSON(w, http.StatusAccepted, map[string]any{
			"id": id, "name": updated.Name, "state": updated.State, "totalBots": updated.TotalBots,
			"agentCount": len(updated.Assignments), "activeAgentCount": len(updated.SucceededAgents), "createdAt": updated.CreatedAt,
		})
		return
	}
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
	now := time.Now()
	items := make([]AgentListItem, 0, len(agents))
	for _, a := range agents {
		items = append(items, buildAgentListItem(a, now))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
	})
}

func buildAgentListItem(agent *AgentNode, now time.Time) AgentListItem {
	item := AgentListItem{
		AgentID:         agent.ID,
		Name:            agent.Name,
		Address:         agent.Address,
		AppVersion:      agent.AppVersion,
		MaxBots:         agent.MaxBots,
		Status:          agent.Status,
		CurrentTaskID:   agent.CurrentTaskID,
		CurrentBots:     agent.CurrentBots,
		StaticInfo:      agent.StaticInfo,
		LastHeartbeatAt: agent.LastHeartbeatAt,
	}
	if !agent.StressUpdatedAt.IsZero() {
		item.StressUpdatedAt = new(agent.StressUpdatedAt)
	}
	if agent.LatestSystem == nil || agent.SystemUpdatedAt.IsZero() {
		return item
	}

	item.SystemUpdatedAt = new(agent.SystemUpdatedAt)
	age := now.Sub(agent.SystemUpdatedAt)
	if age >= 0 {
		item.SystemSnapshotAgeSeconds = new(age.Seconds())
	}
	fresh := (agent.Status == AgentIdle || agent.Status == AgentBusy) &&
		age >= 0 && age <= systemSnapshotFreshFor(agent.SystemInterval)
	item.SystemStale = !fresh
	if !fresh {
		return item
	}

	snapshot := agent.LatestSystem
	item.HostCPUPercent = validPercent(snapshot.HostCPUPercent)
	if _, _, percent, ok := validHostMemory(snapshot); ok {
		item.HostMemPercent = new(percent)
	}
	item.ProcessCPUPercent = validPercent(snapshot.ProcessCPUPercent)
	if snapshot.ProcessRSSBytes != nil {
		item.ProcessRSSBytes = new(*snapshot.ProcessRSSBytes)
	}
	item.ProcessGoroutines = new(snapshot.ProcessGoroutines)
	return item
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
	ctx, cancel := commandContext()
	err := s.scheduleShutdownCommands(ctx, []string{id}, "管理员关闭节点")
	cancel()
	if err != nil {
		stresslog.Warn("关闭命令发送失败", zap.String("agentID", id), zap.Error(err))
		writeError(w, ErrInternal.WithMessage("创建关闭命令失败: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "shutdown_sent"})
}

func (s *AdminServer) handleShutdownAllAgents(w http.ResponseWriter, _ *http.Request) {
	all := s.agents.List()
	var succeeded, failed []string
	succeeded = make([]string, 0)
	failed = make([]string, 0)
	for _, a := range all {
		if a.Status == AgentOffline {
			continue
		}
		succeeded = append(succeeded, a.ID)
	}
	ctx, cancel := commandContext()
	err := s.scheduleShutdownCommands(ctx, succeeded, "管理员批量关闭节点")
	cancel()
	if err != nil {
		failed = append(failed, succeeded...)
		succeeded = succeeded[:0]
		stresslog.Warn("批量关闭命令创建失败", zap.Error(err))
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
		writeJSON(w, http.StatusOK, &StressAggregate{
			Snapshot: &monitor.CollectorSnapshot{TimingDetail: monitor.TimingRTTOnly, Actions: []monitor.ActionSnapshot{}},
			AsOf:     time.Now(),
		})
		return
	}
	agg, err := s.aggregator.AggregateStress(active.ID)
	if err != nil {
		stresslog.Error("聚合压测指标失败", zap.String("taskID", active.ID), zap.Error(err))
		writeError(w, ErrInternal.WithMessage("压测指标聚合失败"))
		return
	}
	public := *agg
	public.Snapshot = agg.Snapshot.PublicCopy()
	writeJSON(w, http.StatusOK, &public)
}

// handleGetMetricsSummary 文本摘要。
func (s *AdminServer) handleGetMetricsSummary(w http.ResponseWriter, r *http.Request) {
	active := s.tasks.ActiveTask()
	if active == nil {
		writeJSON(w, http.StatusOK, map[string]string{"summary": "no active task"})
		return
	}

	agg, err := s.aggregator.AggregateStress(active.ID)
	if err != nil {
		stresslog.Error("聚合压测指标摘要失败", zap.String("taskID", active.ID), zap.Error(err))
		writeError(w, ErrInternal.WithMessage("压测指标聚合失败"))
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Task: %s (%s)\n", active.Name, active.ID)
	fmt.Fprintf(&b, "Total Actions: %d\n", agg.Snapshot.TotalActions)
	if len(agg.Snapshot.Actions) > 0 {
		for _, a := range agg.Snapshot.Actions {
			p50, p99 := "—", "—"
			if a.RTT.P50Ms != nil {
				p50 = fmt.Sprintf("%.1fms", *a.RTT.P50Ms)
			}
			if a.RTT.P99Ms != nil {
				p99 = fmt.Sprintf("%.1fms", *a.RTT.P99Ms)
			}
			fmt.Fprintf(&b, "  %s: count=%d success=%.1f%% p50=%s p99=%s\n",
				a.Name, a.SampleCount, a.SuccessRate*100, p50, p99)
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
				"snapshot":  a.LatestStress.PublicCopy(),
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
	writeJSON(w, http.StatusOK, agent.LatestStress.PublicCopy())
}

func (s *AdminServer) handleGetSystem(w http.ResponseWriter, r *http.Request) {
	active := s.tasks.ActiveTask()
	snap := s.aggregator.AggregateSystem(taskSystemAgentIDs(active))
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
			age := now.Sub(a.SystemUpdatedAt)
			item["isStale"] = age < 0 || age > systemSnapshotFreshFor(a.SystemInterval)
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
	// 当前 adapter 分发格式：每连接一份 *_codec.json + 共享 errors.json。
	for name, data := range cfg.Codecs {
		if err := safeWriteFile("conf/adapter", name, data); err != nil {
			stresslog.Warn("写入基线 codec 失败",
				zap.String("name", name),
				zap.Error(err))
		}
	}
	if len(cfg.ErrorMap) > 0 {
		if err := safeWriteFile("conf/adapter", "errors.json", cfg.ErrorMap); err != nil {
			stresslog.Warn("写入基线 errors.json 失败",
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

// handleBaselineCodecIndex 列出 adapter 基线目录下的 codec/errors 文件名（T3 前端基线同步枚举用）。
// 目录契约：conf/adapter 下只有 *_codec.json 与 errors.json（上传写入侧已拒绝其它）。
// handler 只按 .json 后缀如实列目录，不二次过滤文件名（前端按 errors.json/其余分类）。
func (s *AdminServer) handleBaselineCodecIndex(w http.ResponseWriter, r *http.Request) {
	files, err := listDirFiles("conf/adapter", ".json")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

// handleBaselineCodecFile 提供 adapter 基线目录下的单个 codec/errors 文件。
// 路径形如 /sbot/baseline/adapter/{name}，name = tcp_logic_codec.json / errors.json 等。
func (s *AdminServer) handleBaselineCodecFile(w http.ResponseWriter, r *http.Request) {
	serveBaselineFile(w, r, "conf/adapter", "name")
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

// validateDistributedConcurrency 校验分布式并发能被拆到所有实际分到 bot 的节点。
func validateDistributedConcurrency(rc RobotConfig, assignments []Assignment) error {
	agentCount := assignedAgentCount(assignments)
	if agentCount <= 1 {
		return nil
	}
	if rc.Concurrency > 0 && rc.Concurrency < agentCount {
		return ErrInvalidArgument.WithMessage(fmt.Sprintf("robotConfig.concurrency (%d) must be 0 or >= assigned agents (%d)", rc.Concurrency, agentCount))
	}
	if rc.RampUp == nil {
		return nil
	}
	for i, s := range rc.RampUp.Stages {
		if s.Concurrency > 0 && s.Concurrency < agentCount {
			return ErrInvalidArgument.WithMessage(fmt.Sprintf("rampUp.stages[%d].concurrency (%d) must be 0 or >= assigned agents (%d)", i, s.Concurrency, agentCount))
		}
	}
	return nil
}

func assignedAgentCount(assignments []Assignment) int {
	count := 0
	for _, a := range assignments {
		if a.TotalBots > 0 {
			count++
		}
	}
	return count
}

// splitGlobalValues 将全局并发按 Agent 分到的 bot 占比分摊，保证总和严格等于 global。
//
// global <= 0 时保留 0 语义（不限制）。global > 0 时，每个分到 bot 的 Agent 至少为 1，
// 因此调用前需要保证 global >= 有效分配节点数。
func splitGlobalValues(global, totalBots int, assignments []Assignment) map[string]int {
	out := make(map[string]int, len(assignments))
	if global <= 0 || totalBots <= 0 || len(assignments) == 0 {
		return out
	}

	used := 0
	fracs := make([]float64, len(assignments))
	for i, a := range assignments {
		if a.TotalBots <= 0 {
			continue
		}
		exact := float64(global) * float64(a.TotalBots) / float64(totalBots)
		floor := max(int(math.Floor(exact)), 1)
		out[a.AgentID] = floor
		used += floor
		fracs[i] = exact - math.Floor(exact)
	}

	for remainder := global - used; remainder > 0; remainder-- {
		bestIdx := -1
		bestFrac := -1.0
		for i, a := range assignments {
			if a.TotalBots <= 0 {
				continue
			}
			if fracs[i] > bestFrac {
				bestFrac = fracs[i]
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			break
		}
		out[assignments[bestIdx].AgentID]++
		fracs[bestIdx] = -1
	}
	return out
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
func scaleRampUp(cfg *RampUpConfig, totalBots, assignedBots int, assignments []Assignment, agentID string) *RampUpConfig {
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
		floor := max(int(math.Floor(exact)), 0)
		counts[i] = floor
		fracs[i] = exact - float64(floor)
		used += floor
	}
	// 把剩余的 bot 按余量从大到小补到各 stage，确保总和等于 assignedBots
	remainder := assignedBots - used
	for range remainder {
		bestIdx := -1
		bestFrac := -1.0
		for i := range n {
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
		stageConcurrency := s.Concurrency
		if stageConcurrency > 0 {
			stageConcurrency = splitGlobalValues(stageConcurrency, totalBots, assignments)[agentID]
		}
		scaled.Stages = append(scaled.Stages, RampUpStage{
			Count:       counts[i],
			Concurrency: stageConcurrency,
			Reset:       s.Reset,
			HoldSec:     s.HoldSec,
		})
	}
	return scaled
}
