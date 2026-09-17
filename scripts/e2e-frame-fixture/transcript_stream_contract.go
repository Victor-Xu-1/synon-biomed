package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptFixtureChunkLimit = 128
	transcriptFixtureByteLimit  = 64 * 1024
	transcriptFixtureRefLimit   = 20
)

type transcriptStreamFixtureRequest struct {
	Run          *transcriptStreamFixtureRun          `json:"run,omitempty"`
	BatchID      string                               `json:"batchId"`
	Text         string                               `json:"text,omitempty"`
	Chunks       []string                             `json:"chunks,omitempty"`
	Copy         *transcriptStreamingParityCopy       `json:"copy,omitempty"`
	Recovery     *transcriptStreamingRecoveryCopy     `json:"recovery,omitempty"`
	ArtifactRefs []transcriptFixtureArtifactReference `json:"artifactRefs,omitempty"`
}

type transcriptStreamingParityCopy struct {
	Thinking string `json:"thinking"`
	Search   string `json:"search"`
	Compute  string `json:"compute"`
	Final    string `json:"final"`
}

type transcriptStreamingRecoveryCopy struct {
	Intro    string `json:"intro"`
	First    string `json:"first"`
	Recovery string `json:"recovery"`
	Second   string `json:"second"`
	Final    string `json:"final"`
}

type transcriptFixtureArtifactReference struct {
	ArtifactID string `json:"artifactId"`
	VersionID  string `json:"versionId"`
}

// transcriptStreamFixtureRun is an opaque browser-fixture capability. It is
// returned only to the test process and lets later fixture invocations append
// to the exact runner attempt without shipping a test-only hook in the UI.
type transcriptStreamFixtureRun struct {
	FrameID                 string                       `json:"frameId"`
	RunID                   string                       `json:"runId"`
	StreamUID               string                       `json:"streamUid"`
	OwnerID                 string                       `json:"ownerId"`
	RunnerID                string                       `json:"runnerId"`
	Attempt                 int64                        `json:"attempt"`
	ClaimToken              string                       `json:"claimToken"`
	ClaimedInputRevision    int64                        `json:"claimedInputRevision"`
	ResumeSource            transcriptstore.ResumeSource `json:"resumeSource"`
	ResumeCheckpoint        int64                        `json:"resumeCheckpoint"`
	ResumeCheckpointAttempt int64                        `json:"resumeCheckpointAttempt"`
	ClaimedAt               time.Time                    `json:"claimedAt"`
	ExpiresAt               time.Time                    `json:"expiresAt"`
}

func isTranscriptStreamFixtureAction(action string) bool {
	switch action {
	case "begin-transcript-stream", "append-transcript-deltas", "append-transcript-assistant",
		"begin-streaming-parity", "complete-streaming-parity",
		"begin-streaming-recovery", "complete-streaming-recovery":
		return true
	default:
		return false
	}
}

func validateTranscriptStreamFixtureRequest(request *fixtureRequest) error {
	if request == nil || request.Transcript == nil || request.FrameID == "" || len(request.FrameID) > fixtureIDLimit {
		return errors.New("transcript stream fixture requires bounded frameId and transcript input")
	}
	if request.Status != nil || request.Name != nil || request.TaskSummary != nil || request.StatusDescription != nil ||
		request.Model != nil || request.OutputData != nil || request.ContextData != nil || request.ToolID != "" ||
		len(request.Questions) != 0 || request.HistoryCount != 0 || request.CutoverID != "" {
		return errors.New("transcript stream fixture does not accept other frame fixture fields")
	}
	input := request.Transcript
	input.BatchID = strings.TrimSpace(input.BatchID)
	if !validTranscriptFixtureID(input.BatchID) {
		return errors.New("transcript stream fixture requires a safe bounded batchId")
	}
	switch request.Action {
	case "begin-transcript-stream":
		if input.Run != nil || input.Text != "" || len(input.Chunks) != 0 || input.Copy != nil ||
			input.Recovery != nil || len(input.ArtifactRefs) != 0 {
			return errors.New("begin-transcript-stream accepts only batchId")
		}
	case "append-transcript-deltas":
		if err := validateTranscriptFixtureRun(input.Run, request.FrameID); err != nil {
			return err
		}
		if input.Text != "" || input.Copy != nil || input.Recovery != nil || len(input.ArtifactRefs) != 0 ||
			len(input.Chunks) == 0 || len(input.Chunks) > transcriptFixtureChunkLimit {
			return errors.New("append-transcript-deltas requires only run, batchId, and 1..128 chunks")
		}
		total := 0
		for _, chunk := range input.Chunks {
			if strings.TrimSpace(chunk) == "" || !validFixtureText(chunk) {
				return errors.New("append-transcript-deltas contains invalid chunk text")
			}
			total += len(chunk)
		}
		if total > transcriptFixtureByteLimit {
			return errors.New("append-transcript-deltas exceeds the fixture byte limit")
		}
	case "append-transcript-assistant":
		if err := validateTranscriptFixtureRun(input.Run, request.FrameID); err != nil {
			return err
		}
		if strings.TrimSpace(input.Text) == "" || !validFixtureText(input.Text) || len(input.Chunks) != 0 ||
			input.Copy != nil || input.Recovery != nil || len(input.ArtifactRefs) != 0 {
			return errors.New("append-transcript-assistant requires only run, batchId, and bounded text")
		}
	case "begin-streaming-parity":
		if err := validateTranscriptFixtureRun(input.Run, request.FrameID); err != nil {
			return err
		}
		if input.Run.RunID != input.BatchID || input.Text != "" || len(input.Chunks) != 0 || input.Recovery != nil ||
			len(input.ArtifactRefs) != 0 || !validTranscriptStreamingCopy(input.Copy) {
			return errors.New("begin-streaming-parity requires the matching run, copy, and batchId")
		}
	case "complete-streaming-parity":
		if err := validateTranscriptFixtureRun(input.Run, request.FrameID); err != nil {
			return err
		}
		if input.Run.RunID != input.BatchID || input.Text != "" || len(input.Chunks) != 0 || input.Recovery != nil ||
			!validTranscriptStreamingCopy(input.Copy) || len(input.ArtifactRefs) == 0 ||
			len(input.ArtifactRefs) > transcriptFixtureRefLimit {
			return errors.New("complete-streaming-parity requires the matching run, copy, batchId, and artifact references")
		}
		seen := map[string]bool{}
		for index := range input.ArtifactRefs {
			ref := &input.ArtifactRefs[index]
			ref.ArtifactID = strings.TrimSpace(ref.ArtifactID)
			ref.VersionID = strings.TrimSpace(ref.VersionID)
			key := ref.ArtifactID + "\x00" + ref.VersionID
			if !validTranscriptFixtureID(ref.ArtifactID) || !validTranscriptFixtureID(ref.VersionID) || seen[key] {
				return errors.New("complete-streaming-parity contains an invalid or duplicate artifact reference")
			}
			seen[key] = true
		}
	case "begin-streaming-recovery", "complete-streaming-recovery":
		if err := validateTranscriptFixtureRun(input.Run, request.FrameID); err != nil {
			return err
		}
		if input.Run.RunID != input.BatchID || input.Text != "" || len(input.Chunks) != 0 || input.Copy != nil ||
			len(input.ArtifactRefs) != 0 || !validTranscriptStreamingRecoveryCopy(input.Recovery) {
			return errors.New("streaming recovery requires the matching run, recovery copy, and batchId")
		}
	default:
		return fmt.Errorf("unsupported transcript stream fixture action %q", request.Action)
	}
	return nil
}

func validTranscriptStreamingCopy(copy *transcriptStreamingParityCopy) bool {
	if copy == nil {
		return false
	}
	for _, value := range []string{copy.Thinking, copy.Search, copy.Compute, copy.Final} {
		if strings.TrimSpace(value) == "" || !validFixtureText(value) {
			return false
		}
	}
	return true
}

func validTranscriptStreamingRecoveryCopy(copy *transcriptStreamingRecoveryCopy) bool {
	if copy == nil {
		return false
	}
	for _, value := range []string{copy.Intro, copy.First, copy.Recovery, copy.Second, copy.Final} {
		if strings.TrimSpace(value) == "" || !validFixtureText(value) {
			return false
		}
	}
	return true
}

func validTranscriptFixtureID(value string) bool {
	if value == "" || len(value) > fixtureIDLimit || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._:-", char) {
			continue
		}
		return false
	}
	return true
}

func validateTranscriptFixtureRun(run *transcriptStreamFixtureRun, frameID string) error {
	if run == nil || run.FrameID != frameID || !validTranscriptFixtureID(run.RunID) ||
		strings.TrimSpace(run.StreamUID) == "" || strings.TrimSpace(run.OwnerID) == "" ||
		strings.TrimSpace(run.RunnerID) == "" || strings.TrimSpace(run.ClaimToken) == "" ||
		run.Attempt <= 0 || run.ClaimedInputRevision <= 0 || run.ExpiresAt.IsZero() {
		return errors.New("complete transcript stream fixture run is required")
	}
	return nil
}
