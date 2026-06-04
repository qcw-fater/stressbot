package agent

import (
	"context"
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
		Addr:    fmt.Sprintf(":%d", a.cfg.Port),
		Handler: recoverMiddleware(mux),
	}

	// 先尝试绑定端口，确保端口可用后再启动 goroutine
	listener, err := net.Listen("tcp", a.httpSrv.Addr)
	if err != nil {
		return fmt.Errorf("端口 %d 绑定失败: %w", a.cfg.Port, err)
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

	a.mu.Lock()
	if a.currentTask != nil {
		taskID := a.currentTask.TaskID
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"error":         "task already running",
			"currentTaskId": taskID,
		})
		return
	}
	a.mu.Unlock()

	stresslog.Info("[AGENT] 收到任务下发",
		zap.String("taskID", task.TaskID),
		zap.String("name", task.TaskName),
		zap.Int("totalBots", task.TotalBots),
		zap.Int("startNumber", task.StartNumber))

	// 返回 202 Accepted，异步执行
	w.WriteHeader(http.StatusAccepted)

	// 在协程外 Add，避免与 shutdown 的 taskWG.Wait 形成竞态（
	// "Add 完之前 Wait 已经在 0 上返回"的场景）。
	a.taskWG.Add(1)
	utils.GetWorkPool().Go(func() {
		defer a.taskWG.Done()
		a.executeTask(a.ctx, &task)
	})
}

func (a *Agent) handleStop(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	if a.currentTask == nil {
		a.mu.Unlock()
		http.Error(w, "no task running", http.StatusConflict)
		return
	}
	taskID := a.currentTask.TaskID
	a.mu.Unlock()

	stresslog.Info("[AGENT] 收到停止命令", zap.String("taskID", taskID))
	a.cancelCurrentTask("Admin stop command")
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

func writeJSONError(w http.ResponseWriter, code int, format string, args ...interface{}) {
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
