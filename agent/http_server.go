package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"stressbot/logview"
	"stressbot/utils"
	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// startHTTPServer 启动 Agent HTTP 服务器，接收 Admin 推送的命令。
func (a *Agent) startHTTPServer() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/v1/task", a.handleTaskAssign)
	mux.HandleFunc("/agent/v1/stop", a.handleStop)
	mux.HandleFunc("/agent/v1/shutdown", a.handleShutdown)
	mux.HandleFunc("/agent/v1/version", a.handleVersion)
	mux.HandleFunc("/agent/v1/status", a.handleStatus)
	mux.HandleFunc("/agent/v1/logs", a.handleLogs)
	mux.HandleFunc("/agent/v1/logs/files", a.handleListLogFiles)
	mux.HandleFunc("/agent/v1/logs/files/", a.handleDownloadLogFile)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	a.httpSrv = &http.Server{
		Addr:              fmt.Sprintf(":%d", a.cfg.Port),
		Handler:           recoverMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// 先尝试绑定端口，确保端口可用后再启动 goroutine
	listener, err := net.Listen("tcp", a.httpSrv.Addr)
	if err != nil {
		return fmt.Errorf("端口 %d 绑定失败: %w", a.cfg.Port, err)
	}

	if a.controlPlaneTLS != nil {
		listener = tls.NewListener(listener, a.controlPlaneTLS)
	}

	utils.GetWorkPool().Go(func() {
		stresslog.Info("[AGENT] HTTP 服务已启动", zap.Int("port", a.cfg.Port))
		if err := a.httpSrv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			stresslog.Error("[AGENT] HTTP 服务异常退出", zap.Error(err))
		}
	})
	return nil
}

// recoverMiddleware 捕获 handler panic 并写入应用日志，返回标准 500 JSON。
// 避免依赖 net/http 默认 per-request recover（仅写 stderr 且会断开连接而非返回 500）。
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stresslog.Error("[AGENT] HTTP handler panic",
					zap.String("path", r.URL.Path),
					zap.String("method", r.Method),
					zap.Any("panic", rec),
					zap.String("stack", string(debug.Stack())))
				writeJSONError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (a *Agent) handleTaskAssign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var task TaskAssignment
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := task.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stresslog.Info("[AGENT] 收到任务下发",
		zap.String("taskID", task.TaskID),
		zap.String("name", task.TaskName),
		zap.Int("totalBots", task.TotalBots),
		zap.Int("startNumber", task.StartNumber),
		zap.Int("startIndex", *task.StartIndex))

	// 原子预占 + 提交：校验空闲/关闭态、占用 currentTask、建 cancel、taskWG.Add、池提交
	// 在 submitTask 内一致完成，消除 handler 检查与 executeTask 占用之间的 TOCTOU。
	if err := a.submitTask(&task); err != nil {
		busy, isBusy := errors.AsType[*taskBusyError](err)
		switch {
		case isBusy:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"error":         "task already running",
				"currentTaskId": busy.currentTaskID,
			})
		case errors.Is(err, errAgentShuttingDown):
			http.Error(w, "agent 正在关闭，拒绝新任务", http.StatusServiceUnavailable)
		default:
			stresslog.Error("[AGENT] 任务下发失败", zap.String("taskID", task.TaskID), zap.Error(err))
			http.Error(w, "agent 无法调度任务（协程池不可用）", http.StatusServiceUnavailable)
		}
		return
	}

	// 提交成功，返回 202 Accepted，异步执行。
	w.WriteHeader(http.StatusAccepted)
}

func (a *Agent) handleStop(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TaskID string `json:"taskId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.TaskID == "" {
		http.Error(w, "taskId required", http.StatusBadRequest)
		return
	}

	currentTaskID, canceled := a.cancelTask(request.TaskID, "Admin stop command")
	if !canceled {
		if currentTaskID != "" {
			stresslog.Info("[AGENT] 忽略迟到的停止命令",
				zap.String("requestedTaskID", request.TaskID),
				zap.String("currentTaskID", currentTaskID))
		}
		http.Error(w, "task no longer running", http.StatusConflict)
		return
	}

	stresslog.Info("[AGENT] 收到停止命令", zap.String("taskID", currentTaskID))
	w.WriteHeader(http.StatusOK)
}

func (a *Agent) handleShutdown(w http.ResponseWriter, _ *http.Request) {
	stresslog.Info("[AGENT] 收到远程关闭命令")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "shutting_down"})

	// triggerStop 内部用 sync.Once 保护，可安全并发调用
	utils.GetWorkPool().Go(func() {
		a.triggerStop()
	})
}

func (a *Agent) handleVersion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"version": a.cfg.AppVersion,
	})
}

func (a *Agent) handleStatus(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	status := AgentStatusResponse{
		AgentID:    a.id,
		Status:     string(a.status),
		AppVersion: a.cfg.AppVersion,
		Uptime:     time.Since(a.started).Round(time.Second).String(),
	}
	if a.currentTask != nil {
		status.CurrentTaskID = a.currentTask.TaskID
	}
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (a *Agent) handleLogs(w http.ResponseWriter, r *http.Request) {
	rb := logview.GetRingBuffer()
	if rb == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "log ring buffer not enabled"})
		return
	}

	q := r.URL.Query()
	limit := parseIntOrDefault(q.Get("limit"), 200)
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	result := rb.Query(logview.QueryParams{
		AfterSeq: logview.ParseUint64OrDefault(q.Get("afterSeq"), 0),
		Limit:    limit,
	})

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(result)
}

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

func (a *Agent) shutdownHTTPServer(ctx context.Context) {
	if a.httpSrv == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := a.httpSrv.Shutdown(shutdownCtx); err != nil {
		stresslog.Warn("[AGENT] HTTP 服务关闭异常", zap.Error(err))
	}
}

func writeJSONError(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(ErrorResponse{
		Code:    fmt.Sprintf("STATUS_%d", code),
		Message: fmt.Sprintf(format, args...),
	})
}

func (a *Agent) handleListLogFiles(w http.ResponseWriter, r *http.Request) {
	logPath := stresslog.GetLogFilePath()
	if logPath == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "log file not configured")
		return
	}

	dir := filepath.Dir(logPath)
	base := filepath.Base(logPath)
	prefix := strings.TrimSuffix(base, filepath.Ext(base))

	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "read log dir: %v", err)
		return
	}

	type fileInfo struct {
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		ModTime string `json:"modTime"`
	}

	var files []fileInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	if files == nil {
		files = []fileInfo{}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(files)
}

func (a *Agent) handleDownloadLogFile(w http.ResponseWriter, r *http.Request) {
	logPath := stresslog.GetLogFilePath()
	if logPath == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "log file not configured")
		return
	}

	// 提取文件名：/agent/v1/logs/files/{name}
	name := strings.TrimPrefix(r.URL.Path, "/agent/v1/logs/files/")
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || name == ".." {
		writeJSONError(w, http.StatusBadRequest, "invalid file name")
		return
	}

	path := filepath.Join(filepath.Dir(logPath), name)
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "log file not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "stat failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	http.ServeContent(w, r, name, stat.ModTime(), f)
}
