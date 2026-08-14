package task

import (
	"fmt"
	"os"
	"path/filepath"

	json "stressbot/internal/jsonx"
	"stressbot/internal/stresslog"

	"go.uber.org/zap"
)

func taskFilePath(dataDir, taskID string) string {
	return filepath.Join(dataDir, "tasks", taskID+".json")
}

func saveTaskFile(dataDir string, task *Task) error {
	dir := filepath.Join(dataDir, "tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create tasks dir: %w", err)
	}

	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}

	// 原子写入：先写临时文件再 rename
	tmp := taskFilePath(dataDir, task.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}

	dst := taskFilePath(dataDir, task.ID)
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func removeTaskFile(dataDir, taskID string) error {
	return os.Remove(taskFilePath(dataDir, taskID))
}

func loadTaskFiles(dataDir string) ([]*Task, error) {
	dir := filepath.Join(dataDir, "tasks")

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var tasks []*Task
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			stresslog.Warn("[ADMIN] 跳过无法读取的任务文件", zap.String("file", e.Name()), zap.Error(err))
			continue
		}
		var task Task
		if err := json.Unmarshal(data, &task); err != nil {
			stresslog.Warn("[ADMIN] 跳过损坏的任务文件", zap.String("file", e.Name()), zap.Error(err))
			continue
		}
		tasks = append(tasks, &task)
	}
	return tasks, nil
}
