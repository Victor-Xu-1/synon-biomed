package server

import (
	"errors"
	"fmt"
)

const todoRuntimeNamespace = "todos"

type todoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm"`
}

func (s *Server) executeTodoWriteTool(toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.runtimeStore == nil {
		return nil, errors.New("runtime store is not configured")
	}
	newTodos, err := todoListValue(input["todos"])
	if err != nil {
		return nil, err
	}
	scopeKey := todoScopeKey(stringValue(input["agentId"]), stringValue(input["sessionId"]))
	oldTodos := []todoItem{}
	entry, ok, err := s.runtimeStore.Get(todoRuntimeNamespace, scopeKey)
	if err != nil {
		return nil, err
	}
	if ok {
		oldTodos, err = todoListValue(entry.Value)
		if err != nil {
			return nil, fmt.Errorf("stored todo list is invalid: %w", err)
		}
	}
	storedTodos := newTodos
	if todosAllCompleted(newTodos) {
		storedTodos = []todoItem{}
	}
	if _, err := s.runtimeStore.Set(todoRuntimeNamespace, scopeKey, storedTodos); err != nil {
		return nil, err
	}
	return map[string]any{
		"oldTodos":                oldTodos,
		"newTodos":                newTodos,
		"verificationNudgeNeeded": false,
	}, nil
}
