package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
)

const recoveryValidCIF = "data_example\nloop_\n_atom_site.group_PDB\n_atom_site.Cartn_x\n_atom_site.Cartn_y\n_atom_site.Cartn_z\nATOM 1 2 3\n"

func TestSemanticRecoveryValidatesChangedCandidateAgainstSameNativeContract(t *testing.T) {
	fixture := newCorrectionRouteFixture(t)
	fixture.execute(t, correctionEditCall("valid-source", "structure.cif", recoveryValidCIF))
	fixture.execute(t, correctionEditCall("first-rejected-edit", "structure.cif", "data_example\n_entry.id example\n"))
	fixture.execute(t, correctionEditCall("unrelated-success", "other.txt", "unrelated content"))
	// Change all of the model's proposed bytes while retaining the same
	// unsatisfied native structural condition. This is not a corrected route.
	invalid := correctionEditCall("changed-invalid-edit", "structure.cif", "data_another\n_entry.id another\n")
	diagnostics, err := fixture.gateway().ToolCallPreflightDiagnostics(context.Background(), []agentruntime.ToolCall{invalid})
	if err != nil || !strings.Contains(diagnostics[0], "invalid_file_structure") {
		t.Fatalf("new invalid bytes escaped the recorded semantic condition: diagnostics=%v error=%v", diagnostics, err)
	}
	if starts := fixture.execute(t, invalid); starts != 0 {
		t.Fatalf("known invalid variant reached execution: starts=%d", starts)
	}
	data, err := os.ReadFile(filepath.Join(fixture.projectPath, "structure.cif"))
	if err != nil || string(data) != recoveryValidCIF {
		t.Fatal("recovery preflight changed the source")
	}
	valid := correctionEditCall("corrected-edit", "structure.cif", recoveryValidCIF+"# corrected candidate\n")
	if starts := fixture.execute(t, valid); starts != 1 {
		t.Fatalf("actually valid alternative was quarantined: starts=%d", starts)
	}
}

func TestSemanticRecoveryEditConflictNeedsMatchingCurrentBytesAcrossReopen(t *testing.T) {
	fixture := newCorrectionRouteFixture(t)
	fixture.execute(t, correctionEditCall("initial-file", "report.txt", "current unique text"))
	edit := func(id, old, next string) agentruntime.ToolCall {
		raw, _ := json.Marshal(map[string]any{"file_path": "report.txt", "old_string": old, "new_string": next, "human_description": "Updating the affected file"})
		return agentruntime.ToolCall{ID: id, Name: "edit_file", Arguments: raw}
	}
	fixture.execute(t, edit("first-conflict", "absent text", "new one"))
	fixture.reopen(t)
	// The same live claim remains valid after reopening the canonical store.
	fixture.execute(t, correctionEditCall("other-file", "unrelated.txt", "new material elsewhere"))
	invalid := edit("another-conflict", "still absent", "new two")
	diagnostics, err := fixture.gateway().ToolCallPreflightDiagnostics(context.Background(), []agentruntime.ToolCall{invalid})
	if err != nil || !strings.Contains(diagnostics[0], "edit_conflict") {
		t.Fatalf("prestart failure vanished after restart or unrelated progress: %v %v", diagnostics, err)
	}
	if starts := fixture.execute(t, invalid); starts != 0 {
		t.Fatalf("nonmatching replacement started: %d", starts)
	}
	if starts := fixture.execute(t, edit("exact-correction", "current unique text", "corrected text")); starts != 1 {
		t.Fatalf("valid current-state correction was blocked: %d", starts)
	}
}

func TestSemanticRecoveryReadSelectorsFailBeforeToolStart(t *testing.T) {
	fixture := newCorrectionRouteFixture(t)
	raw, _ := json.Marshal(map[string]any{"file_path": "input.txt", "recovery_condition_id": strings.Repeat("a", 64), "human_description": "Inspecting the recovery condition"})
	call := agentruntime.ToolCall{ID: "ambiguous-selector", Name: "read_file", Arguments: raw}
	diagnostics, err := fixture.gateway().ToolCallPreflightDiagnostics(context.Background(), []agentruntime.ToolCall{call})
	if err != nil || diagnostics[0] == "" {
		t.Fatalf("conflicting selector reached execution: diagnostics=%v error=%v", diagnostics, err)
	}
	if fixture.execute(t, call) != 0 {
		t.Fatal("ambiguous recovery read started")
	}
}

func TestProgressCannotPublishUnverifiedOperationCompletion(t *testing.T) {
	authority := newSessionRunnerProgressOutcomeAuthority(nil)
	for _, text := range []string{
		"成功下载了结构文件，接下来检查格式。",
		"完成计算，所有结果文件已保存。",
		"The file was successfully downloaded. I will inspect it next.",
		"All output files have been saved.",
		"已成功获取完整数据，下一步继续处理。",
		"已经获取完整数据，下一步继续处理。",
		"Successfully obtained the complete source; I will inspect it next.",
		"The source has been retrieved; I will inspect it next.",
	} {
		got := sessionRunnerPublicProgressNarration(text)
		if got == "" || authority.allows(got) {
			t.Errorf("tool-round completion claim escaped receipt validation: %q", got)
		}
	}
	for _, text := range []string{"正在检查文件格式，然后继续下一步。", "The source is unavailable; I will inspect another route.", "下载尚未成功，将检查来源。", "尚未成功获取数据，将检查来源。", "The source has not been successfully obtained.", "I will successfully retrieve the source after checking its location."} {
		if got := sessionRunnerPublicProgressNarration(text); got != text || !authority.allows(got) {
			t.Errorf("ordinary intent or failure disclosure suppressed: %q", got)
		}
	}
	failed := newSessionRunnerProgressOutcomeAuthority([]agentruntime.Message{{Role: "tool", Content: `{"ok":false,"executed":false,"code":"edit_conflict"}`}})
	preflight := newSessionRunnerProgressOutcomeAuthority([]agentruntime.Message{{Role: "tool", Content: `{"ok":true,"executed":false,"decision_required":true}`}})
	succeeded := newSessionRunnerProgressOutcomeAuthority([]agentruntime.Message{{Role: "tool", Content: `{"ok":true,"records":[{"id":"source-1"}]}`}})
	reused := newSessionRunnerProgressOutcomeAuthority([]agentruntime.Message{{Role: "tool", Content: `{"ok":true,"executed":false,"reused":true}`}})
	claim := "已读取版本记录，下一步核对当前环境。"
	if failed.allows(claim) || preflight.allows(claim) || !succeeded.allows(claim) || !reused.allows(claim) {
		t.Fatal("progress completion authority did not follow successful receipt provenance")
	}
	eventAuthority := newSessionRunnerProgressOutcomeAuthority(nil)
	eventAuthority.observe(agentruntime.Event{Type: agentruntime.EventToolCompleted, Result: `{"ok":true,"records":[{"id":"source-1"}]}`})
	if !eventAuthority.allows(claim) {
		t.Fatal("a persisted successful tool completion did not authorize later observed progress")
	}
}

type semanticInvalidEditModel struct{ calls int }

func (model *semanticInvalidEditModel) Complete(context.Context, agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	model.calls++
	if model.calls > 12 {
		return agentruntime.ModelResponse{}, errors.New("test observed an unbounded invalid-edit loop")
	}
	call := correctionEditCall(fmt.Sprintf("invalid-proposal-%d", model.calls), "structure.cif", fmt.Sprintf("data_invalid_%d\n_entry.id no_atoms\n", model.calls))
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "I will check the input format.", ToolCalls: []agentruntime.ToolCall{call}}}, nil
}

func TestSemanticRecoveryYieldsResumesAndExecutesValidAlternative(t *testing.T) {
	fixture := newCorrectionRouteFixture(t)
	fixture.execute(t, correctionEditCall("initial-source", "structure.cif", recoveryValidCIF))
	run := fixture.run
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	options := SessionRunnerChatOptions{SessionID: run.SessionID, RunnerID: run.Transcript.Claim.RunnerID}
	gateway := fixture.gateway()
	starts := 0
	model := &semanticInvalidEditModel{}
	engine := agentruntime.Engine{Model: model, Tools: gateway, OnEventError: func(event agentruntime.Event) error {
		if event.Type == agentruntime.EventModelResponse {
			return fixture.server.checkpointChatModelToolCalls(options, run, event.ToolCalls)
		}
		if event.Type == agentruntime.EventToolStarted {
			starts++
		}
		return fixture.server.checkpointSessionRunnerToolEvent(ctx, options, run, event)
	}}
	_, runErr := engine.Run(ctx, agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Prepare the valid input."}}, Tools: gateway.toolSchemas, MaxConsecutiveIdenticalToolRounds: 2})
	var noProgress *agentruntime.ToolRoundNoProgressError
	if !errors.As(runErr, &noProgress) || starts != 1 {
		t.Fatalf("semantic failures did not bound one strategy: starts=%d requests=%d error=%v", starts, model.calls, runErr)
	}
	cycle := &SessionRunnerCycleResult{SessionID: run.SessionID, Attempt: run.Attempt}
	handled, err := fixture.server.handleSessionRunnerChatInterruption(ctx, options, cycle, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, run.Transcript, run, nil, &runErr, nil)
	if err != nil || !handled || !cycle.InterruptionAutoResume || cycle.Status != "interrupted" {
		t.Fatalf("failed strategy stopped the logical task: %#v error=%v", cycle, err)
	}
	fixture.reopen(t)
	fixture.resume(t)
	firstInvalid := correctionEditCall("inspect-closed-condition", "structure.cif", "data_invalid_1\n_entry.id no_atoms\n")
	diagnostics, err := fixture.gateway().ToolCallPreflightDiagnostics(context.Background(), []agentruntime.ToolCall{firstInvalid})
	if err != nil || !strings.Contains(diagnostics[0], "invalid_file_structure") {
		t.Fatalf("generic route closure erased the precise native recovery condition: %v %v", diagnostics, err)
	}
	if starts := fixture.execute(t, correctionEditCall("valid-alternative", "structure.cif", recoveryValidCIF+"# corrected\n")); starts != 1 {
		t.Fatalf("valid alternative did not proceed after durable recovery: %d", starts)
	}
	data, err := os.ReadFile(filepath.Join(fixture.projectPath, "structure.cif"))
	if err != nil || string(data) != recoveryValidCIF+"# corrected\n" {
		t.Fatalf("valid native result was not persisted: error=%v", err)
	}
	t.Logf("one native failed validation, %d bounded provider proposals, durable handoff/reopen, then a valid native mutation", model.calls)
}

func TestSemanticRecoveryInspectionFailureIsNonExecutingFeedback(t *testing.T) {
	fixture := newCorrectionRouteFixture(t)
	fixture.execute(t, correctionEditCall("initial-source", "structure.cif", recoveryValidCIF))
	fixture.execute(t, correctionEditCall("bad-source", "structure.cif", "data_empty\n"))
	missing := filepath.Join(t.TempDir(), "unavailable-staging-directory")
	t.Setenv("TMPDIR", missing)
	t.Setenv("TMP", missing)
	t.Setenv("TEMP", missing)
	diagnostics, err := fixture.gateway().ToolCallPreflightDiagnostics(context.Background(), []agentruntime.ToolCall{correctionEditCall("corrected", "structure.cif", recoveryValidCIF)})
	if err != nil || !strings.Contains(diagnostics[0], "native_recovery_inspection_unavailable") {
		t.Fatalf("temporary inspection failure became a task-terminal error: %v %v", diagnostics, err)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(diagnostics[0]), &value); err != nil || value["executed"] != false || value["ok"] != false {
		t.Fatal("unavailable validation falsely claimed execution")
	}
}

func TestSemanticRecoveryNativeCoordinateFailureRetainsSource(t *testing.T) {
	fixture := newCorrectionRouteFixture(t)
	const atom = "ATOM      1  C1  LIG A   1       1.250  -2.500   3.750  1.00  0.00      0.000 C\n"
	fixture.execute(t, correctionEditCall("valid-coordinate", "structure.pdb", atom))
	fixture.execute(t, correctionEditCall("invalid-coordinate", "structure.pdb", strings.Replace(atom, "1.250", "  NaN", 1)))
	invalid := correctionEditCall("different-invalid-coordinate", "structure.pdb", strings.Replace(atom, "-2.500", "  +Inf", 1))
	diagnostics, err := fixture.gateway().ToolCallPreflightDiagnostics(context.Background(), []agentruntime.ToolCall{invalid})
	if err != nil || !strings.Contains(diagnostics[0], "invalid_file_structure") || !strings.Contains(diagnostics[0], "invalid atom coordinate") {
		t.Fatalf("changed invalid coordinate escaped the native condition: %v %v", diagnostics, err)
	}
	if fixture.execute(t, invalid) != 0 {
		t.Fatal("known invalid coordinates reached execution")
	}
	data, err := os.ReadFile(filepath.Join(fixture.projectPath, "structure.pdb"))
	if err != nil || string(data) != atom {
		t.Fatal("failed recovery changed the original validated bytes")
	}
}
