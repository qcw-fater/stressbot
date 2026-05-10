package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"stressbot/logview"
	stresslog "stressbot/utils/log"
	"stressbot/utils"

	"go.uber.org/zap"
)

// startHTTPServer 启动 Agent HTTP 服务器，接收 Admin 推送的命令。
func (a *Agent) startHTTPServer() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/v1/task", a.handleTaskAssign)
	mux.HandleFunc("/agent/v1/stop", a.handleStop)
	mux.HandleFunc("/agent/v1/version", a.handleVersion)
	mux.HandleFunc("/agent/v1/status", a.handleStatus)
	mux.HandleFunc("/agent/v1/logs", a.handleLogs)
	mux.HandleFunc("/agent/v1/logs/files", a.handleListLogFiles)
	mux.HandleFunc("/agent/v1/logs/files/", a.handleDownloadLogFile)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	a.httpSrv = &http.Server{
		Addr:    a.cfg.ListenAddr,
		Handler: mux,
	}

	a.wg.Add(1)
	utils.GetWorkPool().Go(func() {
		defer a.wg.Done()
		stresslog.Info("[AGENT] HTTP 服务已启动", zap.String("addr", a.cfg.ListenAddr))
		if err := a.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			stresslog.Error("[AGENT] HTTP 服务异常退出", zap.Error(err))
		}
	})
	return nil
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
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"error":          "task already running",
			"currentTaskId": a.currentTask.TaskID,
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

	utils.GetWorkPool().Go(func() {
		a.wg.Add(1)
		defer a.wg.Done()
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
	if a.taskCancel != nil {
		a.taskCancel()
	}
	w.WriteHeader(http.StatusOK)
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
