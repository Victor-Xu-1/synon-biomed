package server

import (
	"context"
	"errors"
	"os"
	"time"

	taskstore "synon-go/internal/persistence/tasks"
	"synon-go/internal/tools/shellops"
)

func (s *Server) stopAllBackgroundShells(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("background shell shutdown context is required")
	}
	s.backgroundShellMu.Lock()
	running := make(map[string]*shellops.RunningCommand, len(s.backgroundShells))
	for taskID, command := range s.backgroundShells {
		running[taskID] = command
	}
	s.backgroundShells = map[string]*shellops.RunningCommand{}
	s.backgroundShellMu.Unlock()

	var closeErr error
	for taskID, command := range running {
		if err := ctx.Err(); err != nil {
			return errors.Join(closeErr, err)
		}
		if s.taskStore != nil {
			if task, found, err := s.taskStore.Get(taskID); err != nil {
				closeErr = errors.Join(closeErr, err)
			} else if found && task.Status == "running" {
				status := "stopped"
				metadata := cloneTaskMetadata(task.Metadata)
				metadata["stoppedBy"] = "server_shutdown"
				metadata["stoppedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
				if _, _, _, err := s.taskStore.UpdateWithOptions(taskID, taskstore.UpdateOptions{
					Status: &status, Metadata: metadata, MetadataSet: true,
				}); err != nil {
					closeErr = errors.Join(closeErr, err)
				}
			}
		}
		if command != nil {
			if err := command.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				closeErr = errors.Join(closeErr, err)
			}
		}
	}
	return closeErr
}
