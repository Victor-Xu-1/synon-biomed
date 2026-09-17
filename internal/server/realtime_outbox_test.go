package server

import (
	"context"
	"testing"
	"time"

	"synon-go/internal/outbox"
	workspace "synon-go/internal/persistence/workspace"
)

func startServerRealtimeOutbox(t *testing.T, store *workspace.Store, app *Server) {
	t.Helper()
	dispatcher, err := outbox.NewDispatcher(store, outbox.RealtimeDeliverer{Store: store, Fanout: app}, outbox.Options{
		WorkerID: "server-test-" + t.Name(), Topics: []string{workspace.RealtimeOutboxTopic}, BatchSize: 2,
		Lease: time.Second, DeliveryLimit: 500 * time.Millisecond, PollInterval: 5 * time.Millisecond,
		ErrorBackoff: 5 * time.Millisecond, RetryBase: 5 * time.Millisecond, RetryMax: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("stop realtime outbox: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("realtime outbox did not stop")
		}
	})
}

func waitServerRealtimeOutbox(t *testing.T, store *workspace.Store) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		count, err := store.CountUndeliveredRealtimeOutbox(context.Background())
		if err == nil && count == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("realtime outbox did not drain")
}
