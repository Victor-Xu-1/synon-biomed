package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	taskstore "synon-go/internal/persistence/tasks"
	"time"
)

func (s *Server) executeTaskOutputTool(ctx context.Context, toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.taskStore == nil {
		return map[string]any{"retrieval_status": "missing", "task": nil}, nil
	}
	taskID := strings.TrimSpace(stringValue(input["task_id"]))
	if taskID == "" {
		return nil, errors.New("TaskOutput.task_id is required")
	}
	block := boolValue(input["block"], false)
	timeoutMs := numberValue(input["timeout"])
	if timeoutMs <= 0 {
		timeoutMs = 30000
	}
	if timeoutMs > 600000 {
		timeoutMs = 600000
	}
	task, found, err := s.taskStore.Get(taskID)
	if err != nil {
		return nil, err
	}
	if !found {
		return map[string]any{"retrieval_status": "missing", "task": nil}, nil
	}
	if !block {
		status := "success"
		if taskOutputStillRunning(task.Status) {
			status = "not_ready"
		}
		return map[string]any{"retrieval_status": status, "task": taskOutputPayload(task)}, nil
	}
	deadline := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for taskOutputStillRunning(task.Status) {
		select {
		case <-ctx.Done():
			return map[string]any{"retrieval_status": "timeout", "task": taskOutputPayload(task)}, nil
		case <-deadline.C:
			return map[string]any{"retrieval_status": "timeout", "task": taskOutputPayload(task)}, nil
		case <-ticker.C:
			nextTask, nextFound, err := s.taskStore.Get(taskID)
			if err != nil {
				return nil, err
			}
			if !nextFound {
				return map[string]any{"retrieval_status": "missing", "task": nil}, nil
			}
			task = nextTask
		}
	}
	return map[string]any{"retrieval_status": "success", "task": taskOutputPayload(task)}, nil
}

func (s *Server) executeTaskStopTool(toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.taskStore == nil {
		return nil, errors.New("task store is not configured")
	}
	taskID := strings.TrimSpace(stringValue(input["task_id"]))
	if taskID == "" {
		taskID = strings.TrimSpace(stringValue(input["shell_id"]))
	}
	if taskID == "" {
		return nil, errors.New("TaskStop.task_id is required")
	}
	task, found, err := s.taskStore.Get(taskID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("No task found with ID: %s", taskID)
	}
	if !taskStopCanStop(task.Status) {
		return nil, fmt.Errorf("Task %s is not running (status: %s)", taskID, task.Status)
	}
	stoppedStatus := "stopped"
	metadata := cloneTaskMetadata(task.Metadata)
	metadata["stoppedBy"] = "TaskStop"
	metadata["stoppedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	stoppedTask, _, _, err := s.taskStore.UpdateWithOptions(taskID, taskstore.UpdateOptions{
		Status:      &stoppedStatus,
		Metadata:    metadata,
		MetadataSet: true,
	})
	if err != nil {
		return nil, err
	}
	if running := s.takeBackgroundShell(taskID); running != nil {
		_ = running.Kill()
	}
	command := taskStopCommand(stoppedTask)
	return map[string]any{
		"message":   fmt.Sprintf("Successfully stopped task: %s (%s)", stoppedTask.ID, command),
		"task_id":   stoppedTask.ID,
		"task_type": "go_task",
		"command":   command,
	}, nil
}

func taskStopCanStop(status string) bool {
	switch status {
	case "running":
		return true
	default:
		return false
	}
}

func taskStopCommand(task taskstore.Task) string {
	if value, ok := task.Metadata["command"].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return firstNonEmpty(task.ActiveForm, task.Description, task.Subject, task.Title)
}

func taskOutputStillRunning(status string) bool {
	switch status {
	case "pending", "running", "in_progress", "open":
		return true
	default:
		return false
	}
}

func taskOutputPayload(task taskstore.Task) map[string]any {
	outputPath := "tasks.json"
	if value, ok := task.Metadata["output_path"].(string); ok && strings.TrimSpace(value) != "" {
		outputPath = value
	}
	output := ""
	if value, ok := task.Metadata["output"].(string); ok {
		output = value
	}
	if strings.TrimSpace(output) == "" {
		output = task.Description
	}
	if strings.TrimSpace(output) == "" {
		encoded, err := json.MarshalIndent(task, "", "  ")
		if err == nil {
			output = string(encoded)
		}
	}
	payload := map[string]any{
		"task_id":     task.ID,
		"task_type":   "go_task",
		"status":      task.Status,
		"description": firstNonEmpty(task.Description, task.Subject, task.Title),
		"output_path": outputPath,
		"output":      output,
	}
	if value, ok := task.Metadata["exitCode"]; ok {
		payload["exitCode"] = value
	}
	if value, ok := task.Metadata["error"].(string); ok && strings.TrimSpace(value) != "" {
		payload["error"] = value
	}
	return payload
}
