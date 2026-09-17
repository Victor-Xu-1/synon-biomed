package server

import (
	"sync"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceEventHubAllowsConcurrentPublishAndUnsubscribe(t *testing.T) {
	hub := newWorkspaceEventHub()
	var publishers sync.WaitGroup
	for index := 0; index < 100; index++ {
		_, unsubscribe := hub.Subscribe()
		publishers.Add(1)
		go func(sequence int64) {
			defer publishers.Done()
			hub.Publish(workspace.FrameEvent{FrameID: "frame-1", Sequence: sequence, Type: "frame_created"})
		}(int64(index + 1))
		unsubscribe()
	}
	publishers.Wait()
}
