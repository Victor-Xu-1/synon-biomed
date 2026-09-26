package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestReviewFailureClassificationDoesNotHideMixedIntegrityFailures(t *testing.T) {
	protocol := &sessionReviewerSubmissionProtocolError{Detail: "invalid model submission"}
	storage := errors.New("storage commit failed")
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"protocol", protocol, true},
		{"generation budget", &agentruntime.ToolRoundLimitError{Limit: 20}, true},
		{"storage", storage, false},
		{"forged transport text", errors.New("provider HTTP 503 unavailable"), false},
		{"joined", errors.Join(protocol, storage), false},
		{"wrapped joined", fmt.Errorf("generation: %w", errors.Join(protocol, storage)), false},
		{"persistence carrying protocol", wrapSessionRunnerReviewStageError(protocol), false},
		{"root cancellation", context.Canceled, false},
		{"unclassified", errors.New("unexpected failure"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionReviewerFailureIsAdvisory(tt.err); got != tt.want {
				t.Fatalf("advisory=%t want=%t for %v", got, tt.want, tt.err)
			}
		})
	}
}

func TestRunVerifiedSessionAgentRejectsInvalidReviewPolicyWithUsableCandidate(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	model := &artifactReferenceRepairModel{content: "Candidate answer", testing: t}
	result, err := (&Server{workspaceStore: store}).runVerifiedSessionAgent(
		t.Context(), sessionstore.Session{
			ID: "frame-a", Project: &sessionstore.Project{ID: "project-a"},
			Orchestration: map[string]any{"sessionConfig": map[string]any{"verifier_mode": "on"}},
		}, SessionRunnerChatOptions{}, agentruntime.Engine{Model: model},
		agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Produce an answer."}}},
		sessionRunnerTaskContract{}, &sessionRunnerChatRun{ReviewPolicy: &sessionRunnerResolvedReviewPolicy{Schema: "invalid"}}, nil,
	)
	var reviewErr *sessionRunnerReviewStageError
	if !errors.As(err, &reviewErr) || !strings.Contains(err.Error(), "review policy") || !sessionRunnerReviewCandidateUsable(result) {
		t.Fatalf("candidate hid invalid review authority: result=%#v err=%v", result, err)
	}
}

func TestTranscriptRunnerReviewIntegrityAndAdvisoryDisposition(t *testing.T) {
	for _, scenario := range []string{
		"findings", "unavailable", "inventory_change", "unavailable_inventory_change",
		"finding_write_failure", "reviewer_start_failure", "reviewer_finish_failure",
	} {
		t.Run(scenario, func(t *testing.T) {
			store, repo, db := newTranscriptWebFixture(t)
			root := t.TempDir()
			if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-review", UserID: "owner", Name: "Review"}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "review-root", ProjectID: "project-review", AgentName: "OPERON", Status: "processing", ConversationType: "agent"}); err != nil {
				t.Fatal(err)
			}
			saveArtifact := func(content string) error {
				_, _, err := store.WriteArtifactVersion(t.Context(), workspace.WriteArtifactVersionInput{
					ArtifactID: "report", ProjectID: "project-review", Name: "report.md",
					ContentType: "text/markdown", Content: strings.NewReader(content), CreatedBy: "synon",
					RootFrameID: "review-root", FrameID: "review-root",
				})
				return err
			}
			if err := saveArtifact("original content"); err != nil {
				t.Fatal(err)
			}
			faults := map[string]string{
				"reviewer_start_failure": `CREATE TRIGGER fail_review_start BEFORE INSERT ON frame_events
					WHEN NEW.event_type='verification_started' BEGIN SELECT RAISE(ABORT, 'forced review start failure'); END`,
				"finding_write_failure": `CREATE TRIGGER fail_review_finding BEFORE INSERT ON verification_checks
					BEGIN SELECT RAISE(ABORT, 'forced finding write failure'); END`,
				"reviewer_finish_failure": `CREATE TRIGGER fail_review_finish BEFORE INSERT ON frame_events
					WHEN NEW.event_type='verification_completed' BEGIN SELECT RAISE(ABORT, 'forced review finish failure'); END`,
			}
			if fault := faults[scenario]; fault != "" {
				if _, err := db.Exec(fault); err != nil {
					t.Fatal(err)
				}
			}
			var reviewerCalls atomic.Int32
			modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				message := map[string]any{"role": "assistant", "content": "Candidate answer"}
				if len(request.Messages) > 0 && strings.Contains(request.Messages[0].Content, "You are the REVIEWER") {
					call := reviewerCalls.Add(1)
					if call == 1 {
						switch scenario {
						case "inventory_change", "unavailable_inventory_change":
							if err := saveArtifact("changed while review was running"); err != nil {
								t.Error(err)
							}
						}
					}
					if strings.HasPrefix(scenario, "unavailable") {
						http.Error(w, "review service unavailable", http.StatusServiceUnavailable)
						return
					}
					if call == 1 {
						arguments := `{"human_description":"review complete","findings":[]}`
						if scenario == "findings" {
							arguments = `{"human_description":"claim not supported","findings":[{"msg_idx":0,"claim":"Candidate has no recorded source","verdict":"fail","severity":"high","evidence":"The candidate provides no source.","artifact_version_id":null}]}`
						}
						message = map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
							"id": "review-verdict", "type": "function", "function": map[string]any{"name": "submit_output", "arguments": arguments},
						}}}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
			}))
			defer modelAPI.Close()
			srv := New(Options{FileRoot: root, Workspace: store, Transcript: repo})
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := srv.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			if err := srv.sessionStore.Upsert(sessionstore.Session{
				ID: "review-root", Title: "Review integrity", WorkDir: root,
				Project:       &sessionstore.Project{ID: "project-review", Name: "Review", Path: root, BoundAt: time.Now().UTC()},
				Orchestration: map[string]any{"sessionConfig": map[string]any{"verifier_mode": "off"}},
			}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := srv.submitFrameMessage(store, frameMessageSubmission{
				FrameID: "review-root", MessageUUID: "review-message", ClientMessageID: "review-user",
				Text: "Produce an answer.", RuntimeConfig: map[string]any{"verifier_mode": "on"},
			}); err != nil {
				t.Fatal(err)
			}
			seedAnsweredTaskIntake(t, srv, "owner", "review-root")
			result, err := srv.RunSessionRunnerChatOnce(t.Context(), SessionRunnerChatOptions{
				SessionID: "review-root", RunnerID: "review-runner", Endpoint: modelAPI.URL, Model: "test-model",
				LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 64 << 10, MaxAttempts: 1,
				DisableSkillDiscovery: true, DisableMCPDiscovery: true,
			})
			if err != nil || !result.Claimed {
				t.Fatalf("run not settled: result=%#v err=%v", result, err)
			}
			wantCompleted := scenario == "findings" || scenario == "unavailable"
			if (result.Status == "completed") != wantCompleted {
				t.Fatalf("review disposition lost integrity boundary: result=%#v", result)
			}
			if scenario == "findings" {
				checks, err := store.ListVerificationChecks("review-root", "")
				if err != nil || len(checks) != 1 || checks[0].Verdict != "fail" || checks[0].Status != "unaddressed" {
					t.Fatalf("advisory disposition rewrote raw verdict: checks=%#v err=%v", checks, err)
				}
			}
		})
	}
}
