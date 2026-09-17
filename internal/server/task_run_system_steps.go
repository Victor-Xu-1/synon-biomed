package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"synon-go/internal/agentruntime"
	taskruns "synon-go/internal/persistence/taskruns"
	"synon-go/internal/tools/shellops"
	"time"
)

func (s *Server) executeTaskRunSystemSteps(run taskruns.Record) (bool, taskruns.Record, error) {
	if s == nil || s.taskRunStore == nil || len(run.Steps) == 0 {
		return false, taskruns.Record{}, nil
	}
	for _, step := range run.Steps {
		executor := mapValue(step.Executor)
		if strings.TrimSpace(stringValue(executor["kind"])) != "system" {
			return false, taskruns.Record{}, nil
		}
		switch strings.TrimSpace(stringValue(executor["operation"])) {
		case "record_artifact", "shell_command", "visual_review", "read_files", "web_research", "lsp_diagnostics", "patch", "notebook_edit":
		default:
			return false, taskruns.Record{}, nil
		}
	}
	updated, err := s.taskRunStore.Update(run.RunID, func(record taskruns.Record) (taskruns.Record, error) {
		now := time.Now().UTC().UnixMilli()
		record.ActiveChildren = []taskruns.ActiveChild{}
		for i := range record.Steps {
			step := &record.Steps[i]
			executor := mapValue(step.Executor)
			operation := strings.TrimSpace(stringValue(executor["operation"]))
			switch operation {
			case "record_artifact":
				artifact, evidence, err := s.executeTaskRunRecordArtifactStep(record, *step, now)
				if err != nil {
					step.Status = "failed"
					step.Error = err.Error()
					record.Status = "failed"
					record.Blockers = append(record.Blockers, taskruns.Blocker{Kind: "system_step_failed", Message: err.Error(), StepID: step.ID})
					record.NextActions = []string{"Inspect the failed system step and resume after fixing its input."}
					record.Completion = taskruns.Completion{State: "needs_attention", Verified: false, Confidence: "high", Reason: err.Error(), UpdatedAt: now}
					continue
				}
				step.Status = "completed"
				step.CompletedAt = now
				step.UpdatedAt = now
				step.OutputPath = artifact.Path
				step.Artifacts = append(step.Artifacts, artifact)
				record.Artifacts = append(record.Artifacts, artifact)
				record.EvidenceIndex = append(record.EvidenceIndex, evidence)
				record.ExecutionTrace = append(record.ExecutionTrace, taskruns.TraceEvent{At: now, Event: "system_step_completed", StepID: step.ID, Message: "TaskRun system step recorded durable artifact.", Metadata: map[string]any{"operation": operation, "artifact_path": artifact.Path, "artifact_kind": artifact.Kind}})
			case "shell_command":
				artifact, evidence, err := s.executeTaskRunShellCommandStep(record, *step, now)
				if err != nil {
					step.Status = "failed"
					step.Error = err.Error()
					record.Status = "failed"
					record.Blockers = append(record.Blockers, taskruns.Blocker{Kind: "system_step_failed", Message: err.Error(), StepID: step.ID})
					record.NextActions = []string{"Inspect the failed system step and resume after fixing its input."}
					record.Completion = taskruns.Completion{State: "needs_attention", Verified: false, Confidence: "high", Reason: err.Error(), UpdatedAt: now}
					continue
				}
				step.Status = "completed"
				step.CompletedAt = now
				step.UpdatedAt = now
				step.OutputPath = artifact.Path
				step.Artifacts = append(step.Artifacts, artifact)
				record.Artifacts = append(record.Artifacts, artifact)
				record.EvidenceIndex = append(record.EvidenceIndex, evidence)
				record.ExecutionTrace = append(record.ExecutionTrace, taskruns.TraceEvent{At: now, Event: "system_step_completed", StepID: step.ID, Message: "TaskRun system shell command recorded durable output.", Metadata: map[string]any{"operation": operation, "artifact_path": artifact.Path, "artifact_kind": artifact.Kind}})
			case "visual_review":
				artifact, evidence, blockers, err := s.executeTaskRunVisualReviewStep(record, *step, now)
				if err != nil {
					step.Status = "failed"
					step.Error = err.Error()
					record.Status = "failed"
					record.Blockers = append(record.Blockers, taskruns.Blocker{Kind: "system_step_failed", Message: err.Error(), StepID: step.ID})
					record.NextActions = []string{"Inspect the failed system step and resume after fixing its input."}
					record.Completion = taskruns.Completion{State: "needs_attention", Verified: false, Confidence: "high", Reason: err.Error(), UpdatedAt: now}
					continue
				}
				step.Status = "completed"
				step.CompletedAt = now
				step.UpdatedAt = now
				step.OutputPath = artifact.Path
				step.Artifacts = append(step.Artifacts, artifact)
				record.Artifacts = append(record.Artifacts, artifact)
				record.EvidenceIndex = append(record.EvidenceIndex, evidence)
				record.Blockers = append(record.Blockers, blockers...)
				record.ExecutionTrace = append(record.ExecutionTrace, taskruns.TraceEvent{At: now, Event: "system_step_completed", StepID: step.ID, Message: "TaskRun visual review evidence collected; assessment is pending.", Metadata: map[string]any{"operation": operation, "artifact_path": artifact.Path, "artifact_kind": artifact.Kind}})
			case "read_files", "web_research", "lsp_diagnostics", "patch", "notebook_edit":
				artifact, evidence, err := s.executeTaskRunOriginalSystemToolStep(record, *step, operation, now)
				if err != nil {
					step.Status = "failed"
					step.Error = err.Error()
					record.Status = "failed"
					record.Blockers = append(record.Blockers, taskruns.Blocker{Kind: "system_step_failed", Message: err.Error(), StepID: step.ID})
					record.NextActions = []string{"Inspect the failed system tool step and resume after fixing its input."}
					record.Completion = taskruns.Completion{State: "needs_attention", Verified: false, Confidence: "high", Reason: err.Error(), UpdatedAt: now}
					continue
				}
				step.Status = "completed"
				step.CompletedAt = now
				step.UpdatedAt = now
				step.OutputPath = artifact.Path
				step.Artifacts = append(step.Artifacts, artifact)
				record.Artifacts = append(record.Artifacts, artifact)
				record.EvidenceIndex = append(record.EvidenceIndex, evidence)
				record.ExecutionTrace = append(record.ExecutionTrace, taskruns.TraceEvent{At: now, Event: "system_step_completed", StepID: step.ID, Message: "TaskRun original system tool step recorded durable output.", Metadata: map[string]any{"operation": operation, "artifact_path": artifact.Path, "artifact_kind": artifact.Kind}})
			}
		}
		if record.Status != "failed" {
			record.Status = "completed"
			record.NextActions = []string{"TaskRun deterministic system steps completed; verification evidence is recorded."}
			record.Completion = taskruns.Completion{State: "pending_self_check", Verified: false, Confidence: "medium", Reason: "Deterministic system steps completed; final verification is pending.", NextAction: "Call TaskRun action=verify.", UpdatedAt: now}
			for i := range record.Acceptance {
				record.Acceptance[i].Status = "passed"
				record.Acceptance[i].UpdatedAt = now
				if record.Acceptance[i].EvidencePath == "" && len(record.Artifacts) > 0 {
					record.Acceptance[i].EvidencePath = record.Artifacts[0].Path
				}
			}
		}
		record.UpdatedAt = now
		record.History = append(record.History, taskruns.HistoryEvent{At: now, Event: "system_steps_completed", Message: "TaskRun deterministic system executor completed."})
		return record, nil
	})
	if err != nil {
		return true, taskruns.Record{}, err
	}
	if updated.Status == "completed" {
		verified, err := s.taskRunStore.Verify(updated.RunID)
		return true, verified, err
	}
	return true, updated, nil
}

func (s *Server) executeTaskRunRecordArtifactStep(record taskruns.Record, step taskruns.Step, now int64) (taskruns.Artifact, taskruns.Evidence, error) {
	executor := mapValue(step.Executor)
	input := mapValue(executor["input"])
	content := stringValue(input["content"])
	if strings.TrimSpace(content) == "" {
		return taskruns.Artifact{}, taskruns.Evidence{}, errors.New("record_artifact requires input.content")
	}
	kind := firstNonEmpty(stringValue(input["artifact_kind"]), stringValue(input["kind"]), "artifact")
	dir, err := s.storageDirectory("taskArtifacts", record.RunID)
	if err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	filename := safeTaskRunArtifactName(step.ID, "artifact") + ".md"
	path := filepath.Join(dir, filename)
	if err := ensurePathWithinRoot(s.fileRoot, path, "taskrun artifact"); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	body := strings.Join([]string{
		"# " + firstNonEmpty(step.Title, step.ID, "TaskRun artifact"),
		"",
		content,
		"",
		"Recorded at: " + time.UnixMilli(now).UTC().Format(time.RFC3339Nano),
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	description := firstNonEmpty(stringValue(input["description"]), "TaskRun system step artifact.")
	artifact := taskruns.Artifact{StepID: step.ID, Kind: kind, Path: path, Description: description, Metadata: map[string]any{"operation": "record_artifact", "bytes": len(body)}}
	evidence := taskruns.Evidence{ID: fmt.Sprintf("evidence-%d", now), StepID: step.ID, Kind: kind, Path: path, Summary: compactText(content, 500), ProducedAt: now, Metadata: map[string]any{"operation": "record_artifact"}}
	return artifact, evidence, nil
}

func (s *Server) executeTaskRunVisualReviewStep(record taskruns.Record, step taskruns.Step, now int64) (taskruns.Artifact, taskruns.Evidence, []taskruns.Blocker, error) {
	executor := mapValue(step.Executor)
	input := mapValue(executor["input"])
	if strings.TrimSpace(stringValue(input["objective"])) == "" {
		input["objective"] = record.Objective
	}
	evidenceItems, collectionBlockers, _, _ := s.collectVisualReviewEvidence(context.Background(), input)
	if len(evidenceItems) == 0 {
		return taskruns.Artifact{}, taskruns.Evidence{}, nil, errors.New("visual_review requires at least one image evidence path")
	}
	firstEvidence := objectMapValue(evidenceItems[0])
	imagePath := stringValue(firstEvidence["path"])
	if strings.TrimSpace(imagePath) == "" {
		return taskruns.Artifact{}, taskruns.Evidence{}, nil, errors.New("visual_review collected evidence without a path")
	}
	metadata := map[string]any{
		"toolName":           "VisualReview",
		"visual_verified":    false,
		"assessment_verdict": "pending",
		"evidence_count":     len(evidenceItems),
		"criteria":           input["criteria"],
	}
	artifact := taskruns.Artifact{StepID: step.ID, Kind: "visual_review", Path: imagePath, Description: "Visual evidence collected; model assessment pending.", Metadata: metadata}
	evidence := taskruns.Evidence{ID: fmt.Sprintf("evidence-%d", now), StepID: step.ID, Kind: "visual_evidence", Path: imagePath, Summary: "VisualReview collected image evidence; call record_assessment before claiming pass.", ProducedAt: now, Metadata: map[string]any{"toolName": "VisualReview", "evidence_count": len(evidenceItems)}}
	blockers := []taskruns.Blocker{{Kind: "visual_assessment_pending", Message: "VisualReview evidence is collected; call VisualReview record_assessment before marking this TaskRun passed.", StepID: step.ID}}
	for _, blocker := range collectionBlockers {
		entry := objectMapValue(blocker)
		if len(entry) == 0 {
			continue
		}
		blockers = append(blockers, taskruns.Blocker{Kind: firstNonEmpty(stringValue(entry["kind"]), "visual_review_blocker"), Message: firstNonEmpty(stringValue(entry["message"]), "VisualReview evidence collection blocker."), StepID: step.ID})
	}
	return artifact, evidence, blockers, nil
}

func (s *Server) executeTaskRunShellCommandStep(record taskruns.Record, step taskruns.Step, now int64) (taskruns.Artifact, taskruns.Evidence, error) {
	executor := mapValue(step.Executor)
	input := mapValue(executor["input"])
	command := stringValue(input["command"])
	if strings.TrimSpace(command) == "" {
		return taskruns.Artifact{}, taskruns.Evidence{}, errors.New("shell_command requires input.command")
	}
	if err := shellops.CheckSafety("Shell", command, nil); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	result, err := shellops.ExecuteShellCommand(context.Background(), s.fileRoot, "Shell", command, stringValue(input["workdir"]), numberValue(input["timeout"]))
	if err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	content := taskRunShellArtifactContent(command, result)
	kind := "shell_output"
	description := firstNonEmpty(stringValue(input["description"]), "TaskRun system shell output.")
	dir, err := s.storageDirectory("taskArtifacts", record.RunID)
	if err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	path := filepath.Join(dir, safeTaskRunArtifactName(step.ID, kind)+".md")
	if err := ensurePathWithinRoot(s.fileRoot, path, "taskrun shell artifact"); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	artifact := taskruns.Artifact{StepID: step.ID, Kind: kind, Path: path, Description: description, Metadata: map[string]any{"operation": "shell_command", "command": command, "exitCode": result.ExitCode, "stdoutBytes": result.StdoutBytes, "stderrBytes": result.StderrBytes}}
	evidence := taskruns.Evidence{ID: fmt.Sprintf("evidence-%d", now), StepID: step.ID, Kind: kind, Path: path, Summary: compactText(result.Stdout, 500), ProducedAt: now, Metadata: map[string]any{"operation": "shell_command", "exitCode": result.ExitCode}}
	return artifact, evidence, nil
}

func (s *Server) executeTaskRunOriginalSystemToolStep(record taskruns.Record, step taskruns.Step, operation string, now int64) (taskruns.Artifact, taskruns.Evidence, error) {
	toolName, input, err := taskRunOriginalSystemToolCall(operation, mapValue(step.Executor))
	if err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	receipt := s.executeExactToolGateway(
		context.Background(), "task-run-system", "",
		directToolGatewayCallID(toolName, input, time.UnixMilli(now)), toolName, input,
		exactServerToolGatewayOptions{
			SuppressHooks:  true,
			DirectExecutor: true,
			AuditExtra: map[string]any{
				"taskRunId": record.RunID, "taskRunStepId": step.ID, "taskRunOperation": operation,
			},
		},
	)
	if receipt.Err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, receipt.Err
	}
	if receipt.Status == "failed" || receipt.Status == "blocked" {
		return taskruns.Artifact{}, taskruns.Evidence{}, errors.New(agentruntime.ToolFailureEventMessage(receipt.Value))
	}
	result := receipt.Value
	if envelope, ok := result.(map[string]any); ok && boolValue(envelope["ok"], false) {
		if nested, found := envelope["result"]; found {
			result = nested
		}
	}
	body, err := json.MarshalIndent(map[string]any{
		"operation": operation,
		"tool":      toolName,
		"input":     input,
		"result":    result,
	}, "", "  ")
	if err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	dir, err := s.storageDirectory("taskArtifacts", record.RunID)
	if err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	path := filepath.Join(dir, safeTaskRunArtifactName(step.ID, operation)+".json")
	if err := ensurePathWithinRoot(s.fileRoot, path, "taskrun system tool artifact"); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return taskruns.Artifact{}, taskruns.Evidence{}, err
	}
	description := firstNonEmpty(stringValue(mapValue(mapValue(step.Executor)["input"])["description"]), "TaskRun original system tool output.")
	artifact := taskruns.Artifact{StepID: step.ID, Kind: operation, Path: path, Description: description, Metadata: map[string]any{"operation": operation, "toolName": toolName, "bytes": len(body)}}
	evidence := taskruns.Evidence{ID: fmt.Sprintf("evidence-%d-%s", now, step.ID), StepID: step.ID, Kind: operation, Path: path, Summary: "TaskRun executed " + toolName + " through a deterministic system step.", ProducedAt: now, Metadata: map[string]any{"operation": operation, "toolName": toolName}}
	return artifact, evidence, nil
}

func taskRunOriginalSystemToolCall(operation string, executor map[string]any) (string, map[string]any, error) {
	input := objectMapValue(executor["input"])
	switch operation {
	case "read_files":
		return "ReadBatch", input, nil
	case "web_research":
		return "web_research", input, nil
	case "lsp_diagnostics":
		if strings.TrimSpace(stringValue(input["operation"])) == "" {
			input["operation"] = "diagnostics"
		}
		return "LSP", input, nil
	case "patch":
		return "Patch", input, nil
	case "notebook_edit":
		input["_taskrun_system"] = true
		return "NotebookEdit", input, nil
	default:
		return "", nil, fmt.Errorf("unsupported TaskRun original system operation: %s", operation)
	}
}

func taskRunShellArtifactContent(command string, result shellops.Result) string {
	parts := []string{
		"# TaskRun Shell Output",
		"",
		"## Command",
		command,
		"",
		"## Exit Code",
		strconv.Itoa(result.ExitCode),
		"",
		"## Stdout",
		result.Stdout,
	}
	if result.Stderr != "" {
		parts = append(parts, "", "## Stderr", result.Stderr)
	}
	return strings.Join(parts, "\n") + "\n"
}

func safeTaskRunArtifactName(stepID string, kind string) string {
	raw := strings.TrimSpace(stepID + "-" + kind)
	if raw == "-" || raw == "" {
		raw = "artifact"
	}
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
