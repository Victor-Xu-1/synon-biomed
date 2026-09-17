package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	runtimekv "synon-go/internal/persistence/runtimekv"
	workspace "synon-go/internal/persistence/workspace"
)

func TestRunDataLifecycleSweepWithReceiptRecordsSuccessfulNoChangePass(t *testing.T) {
	workspaceStore, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	runtimeStore := runtimekv.New(filepath.Join(t.TempDir(), "runtime-state.sqlite"))
	t.Cleanup(func() { _ = runtimeStore.Close() })

	server := &Server{workspaceStore: workspaceStore, runtimeStore: runtimeStore}
	receipt, err := server.runDataLifecycleSweepWithReceipt(
		context.Background(),
		time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "data lifecycle sweep completed status=ok changed=false " +
		"content_snapshots=0 ephemeral_artifacts=0 compute_usage=0 " +
		"superseded_memories=0 resolved_queued_intents=0 realtime_events=0 " +
		"delivered_outbox=0 expired_dead_letters=0 artifact_blobs_scheduled=0 " +
		"runtime_audits=0 runtime_usage=0"
	if receipt != want {
		t.Fatalf("no-change lifecycle receipt=%q, want %q", receipt, want)
	}
}
