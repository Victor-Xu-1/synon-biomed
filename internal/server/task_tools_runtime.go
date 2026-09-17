package server

import (
	"errors"
	"fmt"
	"strings"
)

func (s *Server) executeTaskTool(toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.taskStore == nil {
		return nil, errors.New("task store is not configured")
	}
	switch toolName {
	case "task_create":
		task, err := s.taskStore.Create(stringValue(input["title"]))
		return map[string]any{"task": task}, err
	case "TaskCreate":
		task, err := s.taskStore.CreateWithOptions(taskCreateOptions(input))
		if err != nil {
			return nil, err
		}
		return map[string]any{"task": map[string]any{"id": task.ID, "subject": task.Subject}}, nil
	case "TaskGet":
		task, found, err := s.taskStore.Get(stringValue(input["taskId"]))
		if err != nil {
			return nil, err
		}
		if !found {
			return map[string]any{"task": nil}, nil
		}
		return map[string]any{"task": originalTaskDetail(task)}, nil
	case "task_update":
		task, err := s.taskStore.Update(stringValue(input["id"]), stringValue(input["title"]), stringValue(input["status"]))
		return map[string]any{"task": task}, err
	case "TaskUpdate":
		update, err := taskUpdateOptions(input)
		if err != nil {
			return nil, err
		}
		_, updatedFields, statusChange, err := s.taskStore.UpdateWithOptions(stringValue(input["taskId"]), update)
		if err != nil {
			if strings.Contains(err.Error(), "task not found") {
				return map[string]any{"success": false, "taskId": stringValue(input["taskId"]), "updatedFields": []string{}, "error": "Task not found"}, nil
			}
			return nil, err
		}
		result := map[string]any{
			"success":       true,
			"taskId":        stringValue(input["taskId"]),
			"updatedFields": updatedFields,
		}
		if statusChange != nil {
			result["statusChange"] = statusChange
		}
		return result, nil
	case "task_list":
		tasks, err := s.taskStore.List()
		return map[string]any{"tasks": tasks}, err
	case "TaskList":
		tasks, err := s.taskStore.List()
		if err != nil {
			return nil, err
		}
		originalTasks := make([]map[string]any, 0, len(tasks))
		resolved := map[string]struct{}{}
		for _, task := range tasks {
			if task.Status == "completed" || task.Status == "done" {
				resolved[task.ID] = struct{}{}
			}
		}
		for _, task := range tasks {
			if task.Metadata != nil {
				if internal, _ := task.Metadata["_internal"].(bool); internal {
					continue
				}
			}
			blockedBy := []string{}
			for _, blockerID := range task.BlockedBy {
				if _, ok := resolved[blockerID]; !ok {
					blockedBy = append(blockedBy, blockerID)
				}
			}
			item := map[string]any{
				"id":        task.ID,
				"subject":   task.Subject,
				"status":    task.Status,
				"blockedBy": blockedBy,
			}
			if task.Owner != "" {
				item["owner"] = task.Owner
			}
			originalTasks = append(originalTasks, item)
		}
		return map[string]any{"tasks": originalTasks}, nil
	default:
		return nil, fmt.Errorf("unknown task tool: %s", toolName)
	}
}
