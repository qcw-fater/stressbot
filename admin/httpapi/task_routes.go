package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	adminagent "stressbot/admin/agent"
	"stressbot/admin/apierror"
	admintask "stressbot/admin/task"
	"stressbot/config/validation"
	json "stressbot/internal/jsonx"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"

	"go.uber.org/zap"
)

// taskExpectedAgents 返回该任务合法完成上报的 Agent 集合：优先 SucceededAgents，
// 为空时回退到 Assignments 的 Agent 列表（与完成阈值口径一致）。
// 用于校验 gRPC 最终报告来源，拒绝未分配节点的伪造或迟到报告。
func taskExpectedAgents(t *admintask.Task) map[string]struct{} {
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
func (s *Handler) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	const maxMultipartMB = 32
	if err := r.ParseMultipartForm(int64(maxMultipartMB) << 20); err != nil {
		writeError(w, apierror.ErrInvalidArgument.WithMessage("multipart parse error"))
		return
	}

	name := r.FormValue("name")
	totalBotsStr := r.FormValue("totalBots")
	if name == "" || totalBotsStr == "" {
		writeError(w, apierror.ErrInvalidArgument.WithMessage("name and totalBots required"))
		return
	}
	totalBots, err := strconv.Atoi(totalBotsStr)
	if err != nil || totalBots <= 0 {
		writeError(w, apierror.ErrInvalidArgument.WithMessage("totalBots must be positive integer"))
		return
	}

	var cfg admintask.TaskConfig

	// flow.json（必需）
	flowFile, _, err := r.FormFile("flow.json")
	if err != nil {
		writeError(w, apierror.ErrInvalidArgument.WithMessage("flow.json file required"))
		return
	}
	flowData, err := io.ReadAll(flowFile)
	_ = flowFile.Close() // multipart 文件句柄，ReadAll 后关闭即可
	if err != nil {
		writeError(w, apierror.ErrInvalidArgument.WithMessage("failed to read flow.json"))
		return
	}
	if err := validation.ValidateFlow(flowData); err != nil {
		writeError(w, apierror.ErrInvalidArgument.WithMessage(err.Error()))
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
			if err := validation.ValidateCodec(data); err != nil {
				writeError(w, apierror.ErrInvalidArgument.WithMessage(fmt.Sprintf("%s: %v", name, err)))
				return
			}
			cfg.Codecs[name] = data
		}
	}

	// robotConfig（JSON string）
	if rc := r.FormValue("robotConfig"); rc != "" {
		if err := json.Unmarshal([]byte(rc), &cfg.RobotConfig); err != nil {
			writeError(w, apierror.ErrInvalidArgument.WithMessage("invalid robotConfig JSON"))
			return
		}
	}

	// 校验 rampUp 配置
	if cfg.RobotConfig.RampUp != nil {
		if len(cfg.RobotConfig.RampUp.Stages) == 0 {
			writeError(w, apierror.ErrInvalidArgument.WithMessage("rampUp.stages must not be empty"))
			return
		}
		sum := 0
		for _, s := range cfg.RobotConfig.RampUp.Stages {
			if s.Count <= 0 {
				writeError(w, apierror.ErrInvalidArgument.WithMessage("rampUp.stage.count must be positive"))
				return
			}
			if s.HoldSec < 0 {
				writeError(w, apierror.ErrInvalidArgument.WithMessage("rampUp.stage.holdSec must be >= 0"))
				return
			}
			sum += s.Count
		}
		if sum != totalBots {
			writeError(w, apierror.ErrInvalidArgument.WithMessage(fmt.Sprintf("rampUp stages count sum (%d) must equal totalBots (%d)", sum, totalBots)))
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
			if ae, ok := err.(*apierror.Error); ok && ae.Code == apierror.ErrFlowTemplateNotFound.Code {
				writeError(w, apierror.ErrFlowTemplateNotFound.WithMessage("来源流程模板不存在"))
			} else {
				writeError(w, apierror.ErrInternal.WithMessage(err.Error()))
			}
			return
		}
	}

	// 将上传的资源写入磁盘基线，使前端下次同步时 IDB 与基线一致
	s.writeBaselineFiles(&cfg, flowData)

	task := &admintask.Task{
		ID:             s.nextID(),
		Name:           name,
		State:          admintask.TaskPending,
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

func (s *Handler) handleListTasks(w http.ResponseWriter, r *http.Request) {
	tasks := s.tasks.List()

	// 过滤
	stateFilter := r.URL.Query().Get("state")
	if stateFilter != "" {
		filtered := make([]*admintask.Task, 0, len(tasks))
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

func (s *Handler) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, ok := s.tasks.Get(id)
	if !ok {
		writeError(w, apierror.ErrTaskNotFound)
		return
	}

	writeJSON(w, http.StatusOK, task)
}

// handleGetTaskConfig 任务配置文件下载（Agent 用）。
func (s *Handler) handleGetTaskConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path := r.PathValue("path")

	task, ok := s.tasks.Get(id)
	if !ok {
		writeError(w, apierror.ErrTaskNotFound)
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

func (s *Handler) handleStartTask(w http.ResponseWriter, r *http.Request) {
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
	if sharedUsed && !s.redisEnabled() {
		if _, terr := s.tasks.Transition(id, admintask.TaskStarting, admintask.TaskFailed); terr != nil {
			stresslog.Warn("[ADMIN] 状态转换失败 starting→failed",
				zap.String("taskID", id), zap.Error(terr))
		}
		writeError(w, apierror.ErrSharedUnavailable.WithMessage("任务脚本使用了共享状态(share)，但服务器未配置 Redis，无法启动"))
		return
	}
	s.tasks.Update(id, func(t *admintask.Task) {
		t.SharedUsed = sharedUsed
		t.SharedRunID = id
	})

	// 分配 Agent
	idleAgents := s.agents.ListByStatus(adminagent.AgentIdle)
	assignments, err := s.assigner.Assign(task, idleAgents, task.Config.RobotConfig.StartNumber)
	if err != nil {
		if _, terr := s.tasks.Transition(id, admintask.TaskStarting, admintask.TaskFailed); terr != nil {
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

	if err := admintask.ValidateDistributedConcurrency(task.Config.RobotConfig, assignments); err != nil {
		if _, terr := s.tasks.Transition(id, admintask.TaskStarting, admintask.TaskFailed); terr != nil {
			stresslog.Warn("[ADMIN] 状态转换失败 starting→failed",
				zap.String("taskID", id), zap.Error(terr))
		}
		writeError(w, err)
		return
	}

	// 保存分配方案
	s.tasks.Update(id, func(t *admintask.Task) {
		t.Assignments = assignments
	})

	// 启动 Sampler
	if s.sampler != nil {
		_ = s.sampler.Start(id)
	}

	// 异步推送任务到各 Agent（捕获值，避免 data race）
	taskID := task.ID
	taskName := task.Name
	workpool.GetWorkPool().Go(func() { s.startTaskBackground(taskID, taskName, assignments) })

	writeJSON(w, http.StatusAccepted, map[string]any{
		"taskId":      id,
		"assignments": assignments,
	})
}

func (s *Handler) startTaskBackground(taskID, taskName string, assignments []admintask.Assignment) {
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
		_ = s.tasks.Update(taskID, func(current *admintask.Task) { current.SucceededAgents = succeeded })
		_, err = s.tasks.Transition(taskID, admintask.TaskStarting, admintask.TaskRunning)
		if err == nil {
			s.finishTaskIfFullyReported(taskID)
		}
	}
	if err != nil {
		stresslog.Error("[ADMIN] 创建 gRPC 启动命令失败", zap.String("taskID", taskID), zap.String("taskName", taskName), zap.Error(err))
		_, _ = s.tasks.Transition(taskID, admintask.TaskStarting, admintask.TaskFailed)
		if s.sampler != nil {
			s.sampler.Stop(taskID)
		}
		return
	}
	stresslog.Info("[ADMIN] gRPC 启动命令已创建并调度", zap.String("taskID", taskID), zap.Int("agents", len(succeeded)))
}

func (s *Handler) handleStopTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	task, ok := s.tasks.Get(id)
	if !ok {
		writeError(w, apierror.ErrTaskNotFound)
		return
	}
	if task.State != admintask.TaskRunning {
		writeError(w, apierror.ErrTaskInvalidState.WithMessage(fmt.Sprintf("task is %s, expected running", task.State)))
		return
	}

	if _, err := s.tasks.Transition(id, admintask.TaskRunning, admintask.TaskStopping); err != nil {
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
			_, _ = s.tasks.Transition(id, admintask.TaskStopping, admintask.TaskRunning)
			writeError(w, apierror.ErrInternal.WithMessage("创建停止命令失败: "+err.Error()))
			return
		}
		allReported := s.synthesizeOfflineReports(id)
		if allReported {
			_, _ = s.tasks.Transition(id, admintask.TaskStopping, admintask.TaskStopped)
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

func (s *Handler) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.Delete(id); err != nil {
		writeError(w, err)
		return
	}
	stresslog.Info("[ADMIN] 任务已删除", zap.String("taskID", id))
	w.WriteHeader(http.StatusNoContent)
}

// ── 前端-Agent ──
