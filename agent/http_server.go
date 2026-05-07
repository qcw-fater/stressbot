package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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

	utils.GetWorkPool().Go(func() { a.executeTask(a.ctx, &task) })
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
