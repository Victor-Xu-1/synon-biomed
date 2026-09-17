package taskruns

import (
	"strings"
	"testing"
	"time"
)

func TestVerifyRunsSoftSelfCheckForMissingEvidence(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = fixedTaskRunClock()
	run, err := store.Create(Input{Action: "start", Objective: "ship a durable result", TaskGraph: explicitTestGraph()})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	run, err = store.Update(run.RunID, func(record Record) (Record, error) {
		record.Status = "completed"
		record.ActiveChildren = nil
		for i := range record.Steps {
			record.Steps[i].Status = "completed"
		}
		return record, nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	verified, err := store.Verify(run.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.Completion.State != "completed_with_issues" || verified.Completion.Verified {
		t.Fatalf("completion = %#v", verified.Completion)
	}
	if verified.SelfCheck.Status != "completed_with_issues" || len(verified.SelfCheck.RemainingIssues) != 1 {
		t.Fatalf("self_check = %#v", verified.SelfCheck)
	}
	if verified.SelfCheck.RemainingIssues[0].Kind != "insufficientEvidence" {
		t.Fatalf("issues = %#v", verified.SelfCheck.RemainingIssues)
	}
	if !strings.Contains(strings.Join(verified.NextActions, "\n"), "Resume or repair") {
		t.Fatalf("next actions = %#v", verified.NextActions)
	}
}

func TestCreateRequiresExplicitGraph(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = fixedTaskRunClock()
	_, err := store.Create(Input{Action: "start", Objective: "do not infer a workflow"})
	if err == nil || !strings.Contains(err.Error(), "generate_plan is the planning authority") {
		t.Fatalf("Create() error = %v", err)
	}
}

func TestCreatePersistsExplicitGraphWithoutPlaybookPlanning(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = fixedTaskRunClock()
	run, err := store.Create(Input{
		Action:        "start",
		Objective:     "execute an explicit graph",
		TaskGraph:     explicitTestGraph(),
		Orchestration: map[string]any{"mode": "compatibility"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if run.Playbook != "" || len(run.PlaybookOptions) != 0 {
		t.Fatalf("new TaskRun persisted retired planning fields: %#v", run)
	}
	if run.Orchestration["mode"] != "compatibility" || len(run.Steps) != 1 || run.Steps[0].ID != "explicit" {
		t.Fatalf("explicit TaskRun = %#v", run)
	}
}

func TestVerifyPassesWithEvidenceAndAcceptance(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = fixedTaskRunClock()
	run, err := store.Create(Input{Action: "start", Objective: "ship a durable result", TaskGraph: explicitTestGraph()})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	run, err = store.Update(run.RunID, func(record Record) (Record, error) {
		record.Status = "completed"
		record.ActiveChildren = nil
		for i := range record.Steps {
			record.Steps[i].Status = "completed"
			record.Steps[i].OutputPath = record.OutputPath
		}
		record.Artifacts = append(record.Artifacts, Artifact{StepID: firstStepID(record), Kind: "task_output", Path: record.OutputPath, Description: "durable result"})
		record.EvidenceIndex = append(record.EvidenceIndex, Evidence{ID: "evidence-1", StepID: firstStepID(record), Kind: "task_output", Path: record.OutputPath, Summary: "durable result", ProducedAt: store.nowMillis()})
		return record, nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	verified, err := store.Verify(run.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.Completion.State != "verified" || !verified.Completion.Verified {
		t.Fatalf("completion = %#v", verified.Completion)
	}
	if verified.SelfCheck.Status != "passed" || len(verified.SelfCheck.Issues) != 0 {
		t.Fatalf("self_check = %#v", verified.SelfCheck)
	}
	for _, acceptance := range verified.Acceptance {
		if acceptance.Status != "passed" {
			t.Fatalf("acceptance not passed: %#v", verified.Acceptance)
		}
	}
}

func TestVerifyPassesWithWarningsAfterRecoveredStep(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = fixedTaskRunClock()
	run, err := store.Create(Input{Action: "start", Objective: "ship a recovered result", TaskGraph: explicitTestGraph()})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	run, err = store.Update(run.RunID, func(record Record) (Record, error) {
		record.Status = "completed"
		record.ActiveChildren = nil
		for i := range record.Steps {
			record.Steps[i].Status = "completed"
			record.Steps[i].OutputPath = record.OutputPath
		}
		record.Steps[0].RecoveryCount = 1
		record.Artifacts = append(record.Artifacts, Artifact{StepID: firstStepID(record), Kind: "task_output", Path: record.OutputPath, Description: "recovered result"})
		record.EvidenceIndex = append(record.EvidenceIndex, Evidence{ID: "evidence-1", StepID: firstStepID(record), Kind: "task_output", Path: record.OutputPath, Summary: "recovered result", ProducedAt: store.nowMillis()})
		return record, nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	verified, err := store.Verify(run.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.Completion.State != "verified_with_warnings" || !verified.Completion.Verified {
		t.Fatalf("completion = %#v", verified.Completion)
	}
	if verified.SelfCheck.Status != "passed_with_warnings" || len(verified.SelfCheck.Issues) != 1 {
		t.Fatalf("self_check = %#v", verified.SelfCheck)
	}
	if verified.SelfCheck.Issues[0].Kind != "recovered" {
		t.Fatalf("issues = %#v", verified.SelfCheck.Issues)
	}
}

func TestVerifyFailedTerminalRunNeedsAttention(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = fixedTaskRunClock()
	run, err := store.Create(Input{Action: "start", Objective: "surface failed work", TaskGraph: explicitTestGraph()})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	run, err = store.Update(run.RunID, func(record Record) (Record, error) {
		record.Status = "failed"
		record.ActiveChildren = nil
		for i := range record.Steps {
			record.Steps[i].Status = "completed"
		}
		record.Steps[0].Status = "failed"
		record.Steps[0].Error = "real command exited non-zero"
		return record, nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	verified, err := store.Verify(run.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.Completion.State != "needs_attention" || verified.Completion.Verified {
		t.Fatalf("completion = %#v", verified.Completion)
	}
	if verified.SelfCheck.Status != "needs_attention" || verified.SelfCheck.Issues[0].Kind != "failedStep" {
		t.Fatalf("self_check = %#v", verified.SelfCheck)
	}
}

func TestVerifyFailedRunWithoutFailedStepNeedsAttention(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = fixedTaskRunClock()
	run, err := store.Create(Input{Action: "start", Objective: "surface record-level failure", TaskGraph: explicitTestGraph()})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	run, err = store.Update(run.RunID, func(record Record) (Record, error) {
		record.Status = "failed"
		record.ActiveChildren = nil
		for i := range record.Steps {
			record.Steps[i].Status = "completed"
			record.Steps[i].OutputPath = record.OutputPath
		}
		return record, nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	verified, err := store.Verify(run.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.Completion.State != "needs_attention" || verified.Completion.Verified {
		t.Fatalf("completion = %#v", verified.Completion)
	}
	if verified.SelfCheck.Status != "needs_attention" || verified.SelfCheck.Issues[0].Kind != "runFailed" {
		t.Fatalf("self_check = %#v", verified.SelfCheck)
	}
}

func TestReconcileCompletesOnlyActiveStepAndResumeQueuesNextRunnableStep(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = fixedTaskRunClock()
	run, err := store.Create(Input{
		Action:    "start",
		Objective: "execute a two step workflow",
		TaskGraph: &Graph{Steps: []StepInput{
			{ID: "inspect", Title: "Inspect", Description: "Inspect first", ExpectedOutput: "inspection evidence", Executor: map[string]any{"kind": "agent"}},
			{ID: "implement", Title: "Implement", Description: "Implement second", ExpectedOutput: "implementation evidence", DependsOn: []string{"inspect"}, Executor: map[string]any{"kind": "agent"}},
		}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if run.Steps[0].Status != "running" || run.Steps[1].Status != "pending" {
		t.Fatalf("initial steps = %#v", run.Steps)
	}

	reconciled, err := store.ReconcileWithRunner(run.RunID, "completed", "inspection done")
	if err != nil {
		t.Fatalf("ReconcileWithRunner() error = %v", err)
	}
	if reconciled.Status != "waiting_next_step" || reconciled.Completion.State != "step_completed" {
		t.Fatalf("reconciled status/completion = %#v %#v", reconciled.Status, reconciled.Completion)
	}
	if reconciled.Steps[0].Status != "completed" || reconciled.Steps[1].Status != "pending" {
		t.Fatalf("reconciled steps = %#v", reconciled.Steps)
	}

	resumed, err := store.Resume(run.RunID, "continue with implementation")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.Steps[0].Status != "completed" || resumed.Steps[1].Status != "running" {
		t.Fatalf("resumed steps = %#v", resumed.Steps)
	}
	if len(resumed.ActiveChildren) != 1 || resumed.ActiveChildren[0].StepID != "implement" {
		t.Fatalf("active children = %#v", resumed.ActiveChildren)
	}
}

func TestReconcileWithCancelledRunnerSettlesTaskRunAndAllRunnableSteps(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = fixedTaskRunClock()
	run, err := store.Create(Input{
		Action: "start", Objective: "cancel a delegated workflow",
		TaskGraph: &Graph{Steps: []StepInput{
			{ID: "active", Title: "Active", Description: "Active work", ExpectedOutput: "output", Executor: map[string]any{"kind": "agent"}},
			{ID: "pending", Title: "Pending", Description: "Pending work", ExpectedOutput: "output", DependsOn: []string{"active"}, Executor: map[string]any{"kind": "agent"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := store.ReconcileWithRunner(run.RunID, "cancelled", "")
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Status != "cancelled" || reconciled.Completion.State != "cancelled" || len(reconciled.ActiveChildren) != 0 {
		t.Fatalf("cancelled TaskRun = %#v", reconciled)
	}
	for _, step := range reconciled.Steps {
		if step.Status != "cancelled" {
			t.Fatalf("step %s remained %s after runner cancellation", step.ID, step.Status)
		}
	}
	foundCancelledTrace := false
	for _, event := range reconciled.ExecutionTrace {
		if event.Event == "cancelled" {
			foundCancelledTrace = true
			break
		}
	}
	if !foundCancelledTrace {
		t.Fatalf("cancelled trace = %#v", reconciled.ExecutionTrace)
	}
	lastTrace := reconciled.ExecutionTrace[len(reconciled.ExecutionTrace)-1]
	if lastTrace.Event != "reconciled" || lastTrace.Message != "Runner status: cancelled" {
		t.Fatalf("cancelled reconciliation trace = %#v", reconciled.ExecutionTrace)
	}
}

func TestResumeQueuesSelfCheckRepairStepForRecordLevelIssue(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = fixedTaskRunClock()
	run, err := store.Create(Input{Action: "start", Objective: "repair missing evidence", TaskGraph: explicitTestGraph()})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	run, err = store.Update(run.RunID, func(record Record) (Record, error) {
		record.Status = "completed"
		record.ActiveChildren = nil
		for i := range record.Steps {
			record.Steps[i].Status = "completed"
		}
		return record, nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	verified, err := store.Verify(run.RunID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.SelfCheck.Status != "completed_with_issues" || len(verified.SelfCheck.RemainingIssues) == 0 {
		t.Fatalf("verified self-check = %#v", verified.SelfCheck)
	}

	resumed, err := store.Resume(run.RunID, "add durable evidence")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if len(resumed.Steps) != len(verified.Steps)+1 {
		t.Fatalf("expected repair step, steps = %#v", resumed.Steps)
	}
	repair := resumed.Steps[len(resumed.Steps)-1]
	if repair.Status != "running" || repair.Intent != "repair" || !strings.Contains(repair.Description, "without durable artifacts") {
		t.Fatalf("repair step = %#v", repair)
	}
	if resumed.SelfCheck.RepairsAttempted != 1 || resumed.SelfCheck.StopReason != "repair_queued" {
		t.Fatalf("self-check after resume = %#v", resumed.SelfCheck)
	}
	if resumed.RepairPolicy.Status != "queued" || resumed.RepairPolicy.RoundsAttempted != 1 {
		t.Fatalf("repair policy after resume = %#v", resumed.RepairPolicy)
	}
}

func TestAutoRepairEvaluatesPolicyAndQueuesRepair(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = fixedTaskRunClock()
	run, err := store.Create(Input{Action: "start", Objective: "auto repair missing evidence", TaskGraph: explicitTestGraph()})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	run, err = store.Update(run.RunID, func(record Record) (Record, error) {
		record.Status = "completed"
		record.ActiveChildren = nil
		for i := range record.Steps {
			record.Steps[i].Status = "completed"
		}
		return record, nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	repaired, queued, err := store.AutoRepair(run.RunID, "autonomous repair")
	if err != nil {
		t.Fatalf("AutoRepair() error = %v", err)
	}
	if !queued || repaired.Status != "running" || len(repaired.ActiveChildren) != 1 {
		t.Fatalf("auto repair queued=%v record=%#v", queued, repaired)
	}
	repair := repaired.Steps[len(repaired.Steps)-1]
	if repair.Intent != "repair" || repair.Status != "running" {
		t.Fatalf("repair step = %#v", repair)
	}
	if repaired.RepairPolicy.Status != "queued" || repaired.RepairPolicy.RoundsAttempted != 1 || repaired.RepairPolicy.MaxRounds != 3 {
		t.Fatalf("repair policy = %#v", repaired.RepairPolicy)
	}
	if !strings.Contains(formatMarkdown(repaired), "## Repair Policy") {
		t.Fatalf("markdown missing repair policy")
	}
}

func TestResumeFailedStepIncrementsRecoveryCount(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = fixedTaskRunClock()
	run, err := store.Create(Input{Action: "start", Objective: "recover failed work", TaskGraph: explicitTestGraph()})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	run, err = store.Update(run.RunID, func(record Record) (Record, error) {
		record.Status = "failed"
		record.ActiveChildren = nil
		record.Steps[0].Status = "failed"
		record.Steps[0].Error = "real failure"
		record.Steps[0].Blocker = &Blocker{Kind: "executorFailed", Message: "real failure", StepID: record.Steps[0].ID}
		return record, nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	resumed, err := store.Resume(run.RunID, "retry failed step")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.Steps[0].Status != "running" || resumed.Steps[0].RecoveryCount != 1 || resumed.Steps[0].Blocker != nil || resumed.Steps[0].Error != "" {
		t.Fatalf("resumed failed step = %#v", resumed.Steps[0])
	}
}

func fixedTaskRunClock() func() time.Time {
	now := time.Unix(100, 0).UTC()
	return func() time.Time {
		now = now.Add(time.Second)
		return now
	}
}

func explicitTestGraph() *Graph {
	return &Graph{Steps: []StepInput{{
		ID:              "explicit",
		Title:           "Explicit step",
		Description:     "Execute the caller-authored graph.",
		ExpectedOutput:  "Durable output.",
		AcceptanceCheck: "The explicit step completes or records a blocker.",
		RiskLevel:       "medium",
		MaxRecoveries:   1,
		Executor:        map[string]any{"kind": "agent"},
	}}}
}
