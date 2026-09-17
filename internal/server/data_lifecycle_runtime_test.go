package server

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	runtimekv "synon-go/internal/persistence/runtimekv"
	workspace "synon-go/internal/persistence/workspace"
)

func TestRunDataLifecyclePassSweepsBoundedRuntimeAudits(t *testing.T) {
	workspaceStore, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	runtimeStore := runtimekv.New(filepath.Join(t.TempDir(), "runtime-state.sqlite"))
	t.Cleanup(func() { _ = runtimeStore.Close() })
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := runtimeStore.EditNamespace("tool-gateway-audit", func(entries map[string]runtimekv.Entry) (bool, error) {
		entries["old"] = runtimekv.Entry{
			Namespace: "tool-gateway-audit", Key: "old", Value: map[string]any{"status": "done"},
			Version: 1, UpdatedAt: old,
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: workspaceStore, runtimeStore: runtimeStore}
	report, err := server.runDataLifecyclePass(context.Background(), old.Add(8*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report.Audits != 1 || report.Usage != 0 || !report.Changed() {
		t.Fatalf("lifecycle report=%#v", report)
	}
}

func TestRunDataLifecycleSchedulerWaitsForFirstSweepAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	first := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runDataLifecycleScheduler(ctx, 15*time.Millisecond, time.Hour, func(context.Context) {
			if calls.Add(1) == 1 {
				close(first)
			}
		})
	}()
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("first data lifecycle sweep did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("data lifecycle scheduler did not stop")
	}
	if calls.Load() != 1 {
		t.Fatalf("lifecycle calls=%d", calls.Load())
	}
}
