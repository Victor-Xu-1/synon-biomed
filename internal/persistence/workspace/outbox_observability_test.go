package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRetiredScientificComputeDeadLetterCannotBeRequeued(t *testing.T) {
	store := openOutboxTestStore(t)
	ctx := context.Background()
	event, err := store.EnqueueOutbox(ctx, EnqueueOutboxInput{
		ID: "scientific-terminal-deadletter", IdempotencyKey: "scientific-terminal-deadletter",
		Topic: retiredScientificComputeSubmissionTopic, Type: "scientific.compute.submission.v1",
		AggregateType: "scientific_compute_submission", AggregateID: "job-terminal",
		Payload: json.RawMessage(`{"jobId":"job-terminal"}`), MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed := claimOneOutbox(t, store, "scientific-terminal-worker", time.Second)
	if dead, err := store.RetryOutbox(ctx, event.ID, claimed.ClaimToken, "terminal", 0); err != nil || !dead {
		t.Fatalf("dead=%t err=%v", dead, err)
	}
	if err := store.RequeueOutboxDeadLetter(ctx, event.ID); !errors.Is(err, ErrRetiredScientificComputeDeadLetter) {
		t.Fatalf("requeue err=%v", err)
	}
	stored, err := store.GetOutboxEvent(ctx, event.ID)
	if err != nil || stored.Status != OutboxStatusDeadLetter {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
}

func TestOutboxObservabilityListsAndRequeuesDeadLetters(t *testing.T) {
	store := openOutboxTestStore(t)
	ctx := context.Background()
	event, err := store.EnqueueOutbox(ctx, EnqueueOutboxInput{
		ID: "dead-1", IdempotencyKey: "dead-1", Topic: "runtime.events", PartitionKey: "session-1",
		Type: "runtime.failed", Payload: json.RawMessage(`{"secret":"must-not-be-projected"}`),
		Headers: map[string]string{"Authorization": "Bearer must-not-be-projected"}, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed := claimOneOutbox(t, store, "worker-1", time.Second)
	dead, err := store.RetryOutbox(ctx, event.ID, claimed.ClaimToken, "delivery failed", 0)
	if err != nil || !dead {
		t.Fatalf("dead=%v err=%v", dead, err)
	}
	stats, err := store.OutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DeadLetter != 1 || stats.Pending != 0 || stats.ByTopic["runtime.events"] != 1 || stats.OldestPendingAt != nil {
		t.Fatalf("dead-letter stats=%#v", stats)
	}
	deadLetters, err := store.ListOutboxDeadLetters(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(deadLetters) != 1 || deadLetters[0].ID != event.ID || deadLetters[0].LastError != "delivery failed" {
		t.Fatalf("dead letters=%#v", deadLetters)
	}
	if err := store.RequeueOutboxDeadLetter(ctx, event.ID); err != nil {
		t.Fatal(err)
	}
	requeued, err := store.GetOutboxEvent(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.Status != OutboxStatusPending || requeued.AttemptCount != 0 || requeued.ClaimToken != "" || requeued.LastError != "" || requeued.DeadLetteredAt != nil {
		t.Fatalf("requeued=%#v", requeued)
	}
	stats, err = store.OutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 || stats.DeadLetter != 0 || stats.Ready != 1 || stats.ByTopic["runtime.events"] != 1 || stats.OldestPendingAt == nil {
		t.Fatalf("requeued stats=%#v", stats)
	}
	if err := store.RequeueOutboxDeadLetter(ctx, event.ID); err == nil {
		t.Fatal("pending event was requeued as a dead letter")
	}
}

func TestRecoveryStatusReportsDurableComputeAndKernelState(t *testing.T) {
	store := openOutboxTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	for _, frame := range []CreateFrameInput{
		{ID: "root-1", ProjectID: "project-1", AgentName: "OPERON", Status: "running", ConversationType: "agent"},
		{ID: "child-1", ProjectID: "project-1", ParentFrameID: "root-1", AgentName: "specialist", Status: "running", ConversationType: "agent"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO compute_workbench_jobs(job_id,owner_user_id,project_id,environment,tier_type,provider,state,started_at,provider_family,provider_label)
			VALUES('job-running','user-1','project-1','base','cpu','local','running',?,'local','Local')`, []any{now}},
		{`INSERT INTO compute_pending_terminate(sandbox_id,provider,job_id,enqueued_at,attempts)
			VALUES('sandbox-1','local','job-running',?,2)`, []any{now.UnixMilli()}},
		{`INSERT INTO compute_managed_endpoints(name,owner_user_id,url,port,state,location,skill_name,live_path,start_script,stop_script,approved_script_hash,state_changed_at)
			VALUES('endpoint-1','user-1','http://127.0.0.1:9000',9000,'running','local','skill','/tmp/live','start','stop','hash',?)`, []any{now}},
	}
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ensureKernelSupervisionSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO kernel_child_supervision(
		frame_id,parent_frame_id,root_frame_id,project_id,owner_user_id,tool_use_id,agent_name,task,status,started_at)
		VALUES('child-1','root-1','root-1','project-1','user-1','tool-1','specialist','task','running',?)`, now); err != nil {
		t.Fatal(err)
	}
	status, err := store.RecoveryStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.ComputeJobsByState["running"] != 1 || status.PendingTerminations != 1 ||
		status.ManagedEndpointsByState["running"] != 1 || status.KernelChildrenByState["running"] != 1 {
		t.Fatalf("recovery status=%#v", status)
	}
}
