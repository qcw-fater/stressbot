package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	tmp := taskFilePath(dataDir, task.ID+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}

	dst := taskFilePath(dataDir, task.ID)
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
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
			continue
		}
		var task Task
		if err := json.Unmarshal(data, &task); err != nil {
			continue
		}
		tasks = append(tasks, &task)
	}
	return tasks, nil
}
