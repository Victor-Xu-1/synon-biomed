// Command e2e-frame-fixture applies deterministic browser-test state through
// the Go workspace store. It is a development helper and is not packaged.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	delegatefixture "synon-go/internal/testsupport/delegatefixture"
)

const requestLimit = 1024 * 1024

const (
	fixtureIDLimit       = 256
	fixtureQuestionLimit = 32
	fixtureOptionLimit   = 64
	fixtureTextLimit     = 4096
)

const (
	fixtureSQLiteBusyCode = 5
	fixtureSeedAttempts   = 4
)

type fixtureRequest struct {
	Action            string                          `json:"action,omitempty"`
	FrameID           string                          `json:"frameId"`
	Status            *string                         `json:"status,omitempty"`
	Name              *string                         `json:"name,omitempty"`
	TaskSummary       *string                         `json:"taskSummary,omitempty"`
	StatusDescription *string                         `json:"statusDescription,omitempty"`
	Model             *string                         `json:"model,omitempty"`
	OutputData        *map[string]any                 `json:"outputData,omitempty"`
	ContextData       *map[string]any                 `json:"contextData,omitempty"`
	ToolID            string                          `json:"toolId,omitempty"`
	Questions         []fixtureQuestion               `json:"questions,omitempty"`
	HistoryCount      int                             `json:"historyCount,omitempty"`
	CutoverID         string                          `json:"cutoverId,omitempty"`
	Transcript        *transcriptStreamFixtureRequest `json:"transcript,omitempty"`
}

type fixtureQuestion struct {
	Header      string          `json:"header,omitempty"`
	Question    string          `json:"question"`
	MultiSelect bool            `json:"multi_select,omitempty"`
	Options     []fixtureOption `json:"options,omitempty"`
}

type fixtureOption struct {
	Label       string          `json:"label"`
	Description string          `json:"description,omitempty"`
	Pros        string          `json:"pros,omitempty"`
	Cons        string          `json:"cons,omitempty"`
	Metadata    fixtureMetadata `json:"metadata,omitempty"`
}

type fixtureMetadata struct {
	SMILES string `json:"smiles,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	databasePath, err := fixtureDatabasePath(os.Getenv("SYNON_GO_E2E_WORKSPACE_DB"))
	if err != nil {
		return err
	}
	request, err := decodeRequest(os.Stdin)
	if err != nil {
		return err
	}
	if request.Action == "seed-delegate" {
		fixture, err := seedDelegateFixture(databasePath)
		if err != nil {
			return fmt.Errorf("seed delegate browser fixture: %w", err)
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "fixture": fixture})
	}
	// The browser fixture is a secondary process attached to the already
	// running source preview. Schema migration, backup, and blob reconciliation
	// remain owned by that primary service; attempting them on every short-lived
	// fixture invocation creates avoidable writer contention.
	store, err := workspace.OpenExisting(databasePath)
	if err != nil {
		return fmt.Errorf("open browser fixture workspace: %w", err)
	}
	defer store.Close()
	if request.Action == "seed-ask-user" {
		response, err := seedAskUserFixture(store, request)
		if err != nil {
			return fmt.Errorf("seed ask-user browser fixture: %w", err)
		}
		return json.NewEncoder(os.Stdout).Encode(response)
	}
	if request.Action == "seed-scroll-history" {
		response, err := seedScrollHistoryFixture(store, request)
		if err != nil {
			return fmt.Errorf("seed scroll history browser fixture: %w", err)
		}
		return json.NewEncoder(os.Stdout).Encode(response)
	}
	if isTranscriptStreamFixtureAction(request.Action) {
		response, err := applyTranscriptStreamFixture(store, request)
		if err != nil {
			return fmt.Errorf("apply transcript stream browser fixture: %w", err)
		}
		return json.NewEncoder(os.Stdout).Encode(response)
	}
	if request.Action == "prepare-transcript-rebase" {
		response, err := prepareTranscriptRebaseFixture(store, request)
		if err != nil {
			return fmt.Errorf("prepare transcript rebase browser fixture: %w", err)
		}
		return json.NewEncoder(os.Stdout).Encode(response)
	}
	if request.Action == "activate-transcript-rebase" {
		response, err := activateTranscriptRebaseFixture(store, request)
		if err != nil {
			return fmt.Errorf("activate transcript rebase browser fixture: %w", err)
		}
		return json.NewEncoder(os.Stdout).Encode(response)
	}
	if request.Action == "remove-delegate" {
		_, found, err := store.GetProject(delegatefixture.ProjectID)
		if err != nil {
			return fmt.Errorf("inspect delegate browser fixture: %w", err)
		}
		if found {
			if err := store.DeleteProject(delegatefixture.ProjectID); err != nil {
				return fmt.Errorf("remove delegate browser fixture: %w", err)
			}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"ok": true, "projectId": delegatefixture.ProjectID, "removed": found,
		})
	}
	if _, found, err := store.GetFrame(request.FrameID); err != nil {
		return err
	} else if !found {
		return fmt.Errorf("frame %q does not exist", request.FrameID)
	}

	if request.Status != nil || request.Name != nil {
		if _, err := store.UpdateFrame(request.FrameID, workspace.UpdateFrameInput{Status: request.Status, Name: request.Name}); err != nil {
			return err
		}
	}
	if request.TaskSummary != nil || request.ContextData != nil {
		metadata, found, err := store.GetFrameRuntimeMetadata(request.FrameID)
		if err != nil {
			return err
		}
		if !found {
			metadata = workspace.FrameRuntimeMetadata{ContextData: map[string]any{}}
		}
		if request.TaskSummary != nil {
			metadata.TaskSummary = *request.TaskSummary
		}
		if request.ContextData != nil {
			metadata.ContextData = *request.ContextData
		}
		if _, err := store.SetFrameRuntimeMetadata(request.FrameID, metadata); err != nil {
			return err
		}
	}
	if request.OutputData != nil {
		if err := store.SetFrameOutputData(request.FrameID, *request.OutputData); err != nil {
			return err
		}
	}
	if request.Model != nil || request.StatusDescription != nil {
		if err := store.UpdateFrameRuntimePresentation(request.FrameID, workspace.FrameRuntimePresentationInput{
			Model: request.Model, StatusDescription: request.StatusDescription,
		}); err != nil {
			return err
		}
	}

	frame, found, err := store.GetFrame(request.FrameID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("frame disappeared after browser fixture update")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "frameId": frame.ID})
}

func seedDelegateFixture(databasePath string) (delegatefixture.Fixture, error) {
	var lastErr error
	for attempt := 1; attempt <= fixtureSeedAttempts; attempt++ {
		store, err := workspace.OpenExisting(databasePath)
		if err == nil {
			fixture, seedErr := delegatefixture.Seed(store)
			if seedErr == nil {
				seedErr = activateDelegateFixtureHistories(store, fixture)
			}
			closeErr := store.Close()
			if seedErr == nil && closeErr == nil {
				return fixture, nil
			}
			if seedErr != nil {
				err = seedErr
			} else {
				err = fmt.Errorf("close browser fixture workspace: %w", closeErr)
			}
		}
		lastErr = err
		if !isSQLiteBusy(err) || attempt == fixtureSeedAttempts {
			break
		}
		time.Sleep(time.Duration(attempt*attempt) * 100 * time.Millisecond)
	}
	return delegatefixture.Fixture{}, lastErr
}

func activateDelegateFixtureHistories(store *workspace.Store, fixture delegatefixture.Fixture) error {
	completed := "completed"
	if _, err := store.UpdateFrame(fixture.ParentFrameID, workspace.UpdateFrameInput{Status: &completed}); err != nil {
		return fmt.Errorf("settle parent before transcript bootstrap: %w", err)
	}
	ctx := context.Background()
	repository, err := store.TranscriptRepository(ctx)
	if err == nil {
		var report transcriptstore.NoStreamFrameHistoryReconciliation
		report, err = repository.ReconcileNoStreamFrameHistories(ctx, transcriptstore.ReconcileNoStreamFrameHistoriesInput{
			Limit: 4, AfterOwnerID: fixture.UserID, AfterSessionID: "delegate-fixture-",
		})
		if err == nil && (report.Scanned != 4 || report.PayloadCreated < 3 || report.Blocked > 1 || report.Deferred != 0) {
			err = fmt.Errorf("delegate transcript bootstrap did not converge: %#v", report)
		}
	}
	processing := "processing"
	_, restoreErr := store.UpdateFrame(fixture.ParentFrameID, workspace.UpdateFrameInput{Status: &processing})
	if err != nil || restoreErr != nil {
		return errors.Join(err, restoreErr)
	}
	for _, frameID := range []string{fixture.ParentFrameID, fixture.ChildFrameID} {
		frameContext, found, contextErr := store.GetFrameRealtimeContext(frameID)
		if contextErr != nil || !found {
			if contextErr == nil {
				contextErr = errors.New("frame realtime context is unavailable")
			}
			return fmt.Errorf("verify delegate history %s: %w", frameID, contextErr)
		}
		authority, found, authorityErr := repository.GetFrameAuthorityBySession(ctx, frameContext.UserID, frameID)
		if authorityErr != nil || !found || !authority.TranscriptPayloadActive() {
			if authorityErr == nil {
				authorityErr = fmt.Errorf(
					"payload transcript authority is unavailable: found=%t read=%s write=%s activation=%d genesis=%d stream=%s epoch=%d",
					found, authority.ReadAuthority, authority.WriteAuthority, len(authority.ActivationID), len(authority.GenesisID),
					authority.ActiveStreamUID, authority.ActiveEpoch,
				)
			}
			return fmt.Errorf("verify delegate history %s: %w", frameID, authorityErr)
		}
	}
	return nil
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == fixtureSQLiteBusyCode
}

func decodeRequest(reader io.Reader) (fixtureRequest, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, requestLimit))
	decoder.DisallowUnknownFields()
	var request fixtureRequest
	if err := decoder.Decode(&request); err != nil {
		return fixtureRequest{}, fmt.Errorf("decode browser fixture request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fixtureRequest{}, errors.New("browser fixture request must contain one JSON value")
		}
		return fixtureRequest{}, fmt.Errorf("decode trailing browser fixture data: %w", err)
	}
	request.FrameID = strings.TrimSpace(request.FrameID)
	request.Action = strings.TrimSpace(request.Action)
	if request.Action == "" {
		request.Action = "update-frame"
	}
	if request.Action == "seed-delegate" || request.Action == "remove-delegate" {
		if request.FrameID != "" || request.Status != nil || request.Name != nil || request.TaskSummary != nil ||
			request.StatusDescription != nil || request.Model != nil || request.OutputData != nil || request.ContextData != nil ||
			request.ToolID != "" || len(request.Questions) != 0 || request.HistoryCount != 0 || request.CutoverID != "" ||
			request.Transcript != nil {
			return fixtureRequest{}, fmt.Errorf("%s does not accept frame fixture fields", request.Action)
		}
		return request, nil
	}
	if request.Action == "seed-ask-user" {
		request.ToolID = strings.TrimSpace(request.ToolID)
		if request.FrameID == "" || request.ToolID == "" || len(request.FrameID) > fixtureIDLimit ||
			len(request.ToolID) > fixtureIDLimit || len(request.Questions) == 0 || len(request.Questions) > fixtureQuestionLimit {
			return fixtureRequest{}, errors.New("seed-ask-user requires bounded frameId, toolId, and questions")
		}
		if request.Status != nil || request.Name != nil || request.TaskSummary != nil || request.StatusDescription != nil ||
			request.Model != nil || request.OutputData != nil || request.ContextData != nil || request.HistoryCount != 0 ||
			request.CutoverID != "" || request.Transcript != nil {
			return fixtureRequest{}, errors.New("seed-ask-user does not accept update-frame fields")
		}
		for index := range request.Questions {
			question := &request.Questions[index]
			question.Header = strings.TrimSpace(question.Header)
			question.Question = strings.TrimSpace(question.Question)
			if question.Question == "" || !validFixtureText(question.Header) || !validFixtureText(question.Question) ||
				len(question.Options) > fixtureOptionLimit {
				return fixtureRequest{}, errors.New("seed-ask-user question text is invalid")
			}
			for optionIndex := range question.Options {
				option := &question.Options[optionIndex]
				option.Label = strings.TrimSpace(option.Label)
				option.Description = strings.TrimSpace(option.Description)
				option.Pros = strings.TrimSpace(option.Pros)
				option.Cons = strings.TrimSpace(option.Cons)
				option.Metadata.SMILES = strings.TrimSpace(option.Metadata.SMILES)
				if option.Label == "" || !validFixtureText(option.Label) || !validFixtureText(option.Description) ||
					!validFixtureText(option.Pros) || !validFixtureText(option.Cons) || !validFixtureText(option.Metadata.SMILES) {
					return fixtureRequest{}, errors.New("seed-ask-user option label is invalid")
				}
			}
		}
		return request, nil
	}
	if request.Action == "seed-scroll-history" {
		if request.FrameID == "" || len(request.FrameID) > fixtureIDLimit || request.HistoryCount < 6 ||
			request.HistoryCount > 100 || request.Status != nil || request.Name != nil || request.TaskSummary != nil ||
			request.StatusDescription != nil || request.Model != nil || request.OutputData != nil || request.ContextData != nil ||
			request.ToolID != "" || len(request.Questions) != 0 || request.CutoverID != "" || request.Transcript != nil {
			return fixtureRequest{}, errors.New("seed-scroll-history requires only frameId and historyCount 6..100")
		}
		return request, nil
	}
	if isTranscriptStreamFixtureAction(request.Action) {
		if err := validateTranscriptStreamFixtureRequest(&request); err != nil {
			return fixtureRequest{}, err
		}
		return request, nil
	}
	if request.Action == "prepare-transcript-rebase" {
		if request.FrameID == "" || len(request.FrameID) > fixtureIDLimit || request.HistoryCount < 120 ||
			request.HistoryCount > 500 || request.CutoverID != "" || request.Status != nil || request.Name != nil ||
			request.TaskSummary != nil || request.StatusDescription != nil || request.Model != nil || request.OutputData != nil ||
			request.ContextData != nil || request.ToolID != "" || len(request.Questions) != 0 || request.Transcript != nil {
			return fixtureRequest{}, errors.New("prepare-transcript-rebase requires only frameId and historyCount 120..500")
		}
		return request, nil
	}
	if request.Action == "activate-transcript-rebase" {
		request.CutoverID = strings.TrimSpace(request.CutoverID)
		if request.FrameID == "" || len(request.FrameID) > fixtureIDLimit || len(request.CutoverID) != 64 ||
			request.HistoryCount != 0 || request.Status != nil || request.Name != nil || request.TaskSummary != nil ||
			request.StatusDescription != nil || request.Model != nil || request.OutputData != nil || request.ContextData != nil ||
			request.ToolID != "" || len(request.Questions) != 0 || request.Transcript != nil {
			return fixtureRequest{}, errors.New("activate-transcript-rebase requires only bounded frameId and cutoverId")
		}
		return request, nil
	}
	if request.Action != "update-frame" {
		return fixtureRequest{}, fmt.Errorf("unsupported browser fixture action %q", request.Action)
	}
	if request.FrameID == "" {
		return fixtureRequest{}, errors.New("frameId is required")
	}
	if request.ToolID != "" || len(request.Questions) != 0 || request.HistoryCount != 0 || request.CutoverID != "" ||
		request.Transcript != nil {
		return fixtureRequest{}, errors.New("update-frame does not accept ask-user fields")
	}
	if request.Status == nil && request.Name == nil && request.TaskSummary == nil && request.StatusDescription == nil &&
		request.Model == nil && request.OutputData == nil && request.ContextData == nil {
		return fixtureRequest{}, errors.New("at least one frame fixture field is required")
	}
	return request, nil
}

func prepareTranscriptRebaseFixture(store *workspace.Store, request fixtureRequest) (map[string]any, error) {
	ctx := context.Background()
	frameContext, found, err := store.GetFrameRealtimeContext(request.FrameID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("frame realtime context is unavailable")
		}
		return nil, err
	}
	repository, err := store.TranscriptRepository(ctx)
	if err != nil {
		return nil, err
	}
	stream, found, err := repository.GetFrameStreamBySession(ctx, frameContext.UserID, request.FrameID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("frame transcript stream is unavailable")
		}
		return nil, err
	}
	if err := prepareLegacyTranscriptRebaseAuthority(ctx, repository, stream); err != nil {
		return nil, err
	}
	for index := 0; index < request.HistoryCount; index++ {
		suffix := fmt.Sprintf("%03d", index)
		_, _, _, err := repository.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: "e2e-rebase-client:" + request.FrameID + ":" + suffix,
			FrameEventID:    "e2e-rebase-frame:" + request.FrameID + ":" + suffix,
			MessageUUID:     "e2e-rebase-message:" + request.FrameID + ":" + suffix,
			Text:            "Durable rebase history " + suffix,
		})
		if err != nil {
			return nil, fmt.Errorf("append history event %d: %w", index, err)
		}
	}
	claimed, err := repository.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "e2e-rebase-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		if err == nil {
			err = errors.New("fixture runner claim was not acquired")
		}
		return nil, fmt.Errorf("claim fixture runner: %w", err)
	}
	toolID := "e2e-rebase-ask"
	question := "Keep the migrated history?"
	parked, err := store.ParkAskUser(workspace.ParkAskUserInput{
		FrameID: request.FrameID, ToolID: toolID, ToolName: "AskUserQuestion",
		Questions: []any{map[string]any{
			"header": "History", "question": question,
			"options": []any{
				map[string]any{"label": "Keep", "description": "Keep the durable history."},
				map[string]any{"label": "Review", "description": "Review the durable history first."},
			},
		}},
	})
	if err != nil || parked.AlreadyPending || len(parked.Events) != 3 {
		if err == nil {
			err = errors.New("legacy ask-user fixture was not parked")
		}
		return nil, fmt.Errorf("park legacy ask-user: %w", err)
	}
	answer, err := transcriptstore.NewAskUserResultV1(
		transcriptstore.AskUserActionAnswer, map[string]string{question: "Keep"}, "",
	)
	if err != nil {
		return nil, fmt.Errorf("build legacy AskUser result: %w", err)
	}
	encodedAnswer, err := transcriptstore.EncodeAskUserResultV1(answer)
	if err != nil {
		return nil, fmt.Errorf("encode legacy AskUser result: %w", err)
	}
	if err := repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if _, err := tx.AppendClaimedFrameAskUserReferences(ctx, transcriptstore.AppendClaimedFrameAskUserReferencesInput{
			Claim: claimed.Claim, FrameID: request.FrameID, ToolUseID: toolID,
			ToolUseFrameEventID: parked.Events[0].ID, ToolResultFrameEventID: parked.Events[1].ID,
		}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("bind claimed legacy AskUser references: %w", err)
	}
	resolved, err := store.ResolveCompatibilityPendingInputs(request.FrameID, []workspace.CompatibilityInputResolution{{
		ToolID: toolID, Content: string(encodedAnswer), IsError: true,
	}})
	if err != nil || resolved.Status != "accepted" {
		if err == nil {
			err = errors.New("legacy ask-user result was not accepted")
		}
		return nil, fmt.Errorf("resolve legacy ask-user result: %w", err)
	}
	if _, _, _, err := repository.FinishRunner(ctx, transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "e2e-rebase-finished", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil {
		return nil, fmt.Errorf("finish fixture runner: %w", err)
	}
	audit, _, err := repository.AuditAskUserHistory(ctx, transcriptstore.AuditAskUserHistoryInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, MaxEvents: 1000, MaxCandidates: 256, MaxShadowRows: 1000,
	})
	if err != nil {
		return nil, err
	}
	backfill, _, err := repository.StageAskUserHistoryBackfill(ctx, transcriptstore.StageAskUserHistoryBackfillInput{
		RunID: audit.RunID, StreamUID: stream.UID, OwnerID: stream.OwnerID, BranchID: audit.BranchID,
		MaxEvents: 1000, MaxCandidates: 256, MaxShadowRows: 1000,
	})
	if err != nil {
		return nil, err
	}
	cutover, _, err := repository.PrepareAskUserHistoryCutover(ctx, transcriptstore.PrepareAskUserHistoryCutoverInput{
		BackfillID: backfill.BackfillID, StreamUID: stream.UID, OwnerID: stream.OwnerID,
		MaxBranches: 64, MaxEvents: 1000, MaxCursorRows: 1000, MaxShadowRows: 1000,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "frameId": request.FrameID, "cutoverId": cutover.CutoverID,
		"sourceBranchId": cutover.SourceBranchID, "sourceGeneration": cutover.SourceGeneration,
		"sourceThroughPublicationSequence": cutover.SourceThroughPublicationSequence,
		"eventCount":                       cutover.EventCount, "cursorCount": cutover.CursorCount,
	}, nil
}

func prepareLegacyTranscriptRebaseAuthority(
	ctx context.Context,
	repository *transcriptstore.Repository,
	stream transcriptstore.Stream,
) error {
	return repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		authority, found, err := tx.GetFrameAuthorityBySession(ctx, stream.OwnerID, stream.SessionID)
		if err != nil {
			return fmt.Errorf("read transcript rebase fixture authority: %w", err)
		}
		if !found || authority.ActiveStreamUID != stream.UID || authority.ActiveEpoch != stream.Epoch ||
			authority.AuthorityGeneration != 1 || stream.Epoch != 1 {
			return errors.New("transcript rebase fixture requires a new epoch-one conversation")
		}
		payloadAuthority := authority.TranscriptPayloadActive()
		legacyAuthority := authority.ReadAuthority == "legacy_mixed_v1" && authority.WriteAuthority == "legacy_frame_ref_v1" &&
			len(authority.ActivationID) == 0 && len(authority.GenesisID) == 0
		if !payloadAuthority && !legacyAuthority {
			return errors.New("transcript rebase fixture authority is not eligible")
		}
		var events, attempts, artifactCommits, deliveryIntents int
		for query, target := range map[string]*int{
			`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?`:           &events,
			`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid=?`:  &attempts,
			`SELECT COUNT(*) FROM transcript_artifact_commits WHERE stream_uid=?`: &artifactCommits,
			`SELECT COUNT(*) FROM transcript_delivery_intents WHERE stream_uid=?`: &deliveryIntents,
		} {
			if err := tx.QueryRowContext(ctx, query, stream.UID).Scan(target); err != nil {
				return err
			}
		}
		if events != 0 || attempts != 0 || artifactCommits != 0 || deliveryIntents != 0 {
			return errors.New("transcript rebase fixture requires an empty conversation")
		}
		if legacyAuthority {
			return nil
		}
		deletedAuthority, err := tx.ExecContext(ctx, `DELETE FROM transcript_frame_authority
			WHERE owner_id=? AND session_id=? AND active_stream_uid=? AND active_epoch=? AND authority_generation=1
				AND read_authority='transcript_payload_v1' AND write_authority='transcript_payload_v1'
				AND activation_id IS NULL AND genesis_id=?`,
			stream.OwnerID, stream.SessionID, stream.UID, stream.Epoch, authority.GenesisID)
		if err != nil {
			return err
		}
		if affected, err := deletedAuthority.RowsAffected(); err != nil || affected != 1 {
			return errors.New("transcript rebase fixture authority changed")
		}
		deletedGenesis, err := tx.ExecContext(ctx, `DELETE FROM transcript_payload_genesis_receipts
			WHERE genesis_id=? AND stream_uid=? AND epoch=? AND authority_generation=1`,
			authority.GenesisID, stream.UID, stream.Epoch)
		if err != nil {
			return err
		}
		if affected, err := deletedGenesis.RowsAffected(); err != nil || affected != 1 {
			return errors.New("transcript rebase fixture genesis changed")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO transcript_frame_authority(
			owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
			read_authority,write_authority,activation_id,genesis_id,updated_at)
			VALUES(?,?,?,?,1,'legacy_mixed_v1','legacy_frame_ref_v1',NULL,NULL,?)`,
			stream.OwnerID, stream.SessionID, stream.UID, stream.Epoch, time.Now().UTC())
		return err
	})
}

func activateTranscriptRebaseFixture(store *workspace.Store, request fixtureRequest) (map[string]any, error) {
	ctx := context.Background()
	frameContext, found, err := store.GetFrameRealtimeContext(request.FrameID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("frame realtime context is unavailable")
		}
		return nil, err
	}
	repository, err := store.TranscriptRepository(ctx)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		settlement, err := repository.DeliverySettlementState(ctx, frameContext.UserID, "ws")
		if err != nil {
			return nil, err
		}
		if settlement.Poisoned {
			return nil, errors.New("transcript rebase fixture has poisoned Web delivery")
		}
		if !settlement.ExplicitlyRecoverable {
			break
		}
		if time.Now().After(deadline) {
			return nil, errors.New("transcript rebase fixture Web delivery did not settle")
		}
		time.Sleep(50 * time.Millisecond)
	}
	stream, found, err := repository.GetFrameStreamBySession(ctx, frameContext.UserID, request.FrameID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("frame transcript stream is unavailable")
		}
		return nil, err
	}
	var runningAttempts, unboundArtifacts, unsettledDeliveries int
	quiescenceDeadline := time.Now().Add(10 * time.Second)
	for {
		if err := repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_runner_attempts
				WHERE stream_uid=? AND status='running'`, stream.UID).Scan(&runningAttempts); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_artifact_commits
				WHERE stream_uid=? AND bound_event_id IS NULL`, stream.UID).Scan(&unboundArtifacts); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_delivery_intents
				WHERE stream_uid=? AND status IN ('pending','inflight','failed')`, stream.UID).Scan(&unsettledDeliveries); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return nil, err
		}
		if unsettledDeliveries == 0 || time.Now().After(quiescenceDeadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if runningAttempts != 0 || unboundArtifacts != 0 || unsettledDeliveries != 0 {
		var publicationSequence int64
		var destination, status, lastErrorCode string
		var attemptCount int
		_ = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
			return tx.QueryRowContext(ctx, `SELECT publication_seq,destination,status,attempt_count,
				COALESCE(last_error_code,'') FROM transcript_delivery_intents
				WHERE stream_uid=? AND status IN ('pending','inflight','failed')
				ORDER BY publication_seq LIMIT 1`, stream.UID).Scan(
				&publicationSequence, &destination, &status, &attemptCount, &lastErrorCode)
		})
		return nil, fmt.Errorf(
			"transcript rebase fixture is not quiescent: runners=%d artifacts=%d deliveries=%d; first delivery seq=%d destination=%s status=%s attempts=%d error=%s",
			runningAttempts, unboundArtifacts, unsettledDeliveries, publicationSequence, destination, status, attemptCount,
			lastErrorCode,
		)
	}
	activation, _, err := repository.ActivateAskUserHistoryCutover(ctx,
		transcriptstore.ActivateAskUserHistoryCutoverInput{
			CutoverID: request.CutoverID, OwnerID: frameContext.UserID,
			MaxBranches: 64, MaxEvents: 1000, MaxAttempts: 256, MaxCheckpoints: 1000,
			MaxBranchEvents: 1000, MaxArtifactCommits: 1000, MaxArtifactRefs: 1000, MaxRoutes: 64,
		})
	if err != nil {
		return nil, err
	}
	if activation.SourceStreamUID == "" || activation.TargetStreamUID == "" || activation.SourceStreamUID == activation.TargetStreamUID {
		return nil, errors.New("transcript rebase activation is incomplete")
	}
	return map[string]any{
		"ok": true, "frameId": request.FrameID, "cutoverId": activation.CutoverID,
		"activationId": activation.ActivationSHA256, "targetEpoch": activation.TargetEpoch,
		"authorityGeneration": activation.AuthorityGeneration, "activeBranchId": activation.ActiveBranchID,
		"eventCount": activation.EventCount,
	}, nil
}

func validFixtureText(value string) bool {
	return len(value) <= fixtureTextLimit
}

func fixtureQuestions(questions []fixtureQuestion) ([]any, error) {
	result := make([]any, len(questions))
	for index, question := range questions {
		options := make([]any, len(question.Options))
		for optionIndex, option := range question.Options {
			options[optionIndex] = map[string]any{
				"label": option.Label, "description": option.Description,
			}
		}
		value := map[string]any{
			"header": question.Header, "question": question.Question, "options": options,
			"multiSelect": question.MultiSelect,
		}
		result[index] = value
	}
	return result, nil
}

func seedAskUserFixture(store *workspace.Store, request fixtureRequest) (map[string]any, error) {
	questions, err := fixtureQuestions(request.Questions)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	frameContext, found, err := store.GetFrameRealtimeContext(request.FrameID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("ask-user frame realtime context is unavailable")
		}
		return nil, err
	}
	repository, err := store.TranscriptRepository(ctx)
	if err != nil {
		return nil, err
	}
	stream, found, err := repository.GetFrameStreamBySession(ctx, frameContext.UserID, request.FrameID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("ask-user transcript stream is unavailable")
		}
		return nil, err
	}
	alreadyPending, err := fixtureAskUserAlreadyPending(ctx, repository, stream, request.ToolID, questions)
	if err != nil {
		return nil, err
	}
	if alreadyPending {
		return map[string]any{
			"ok": true, "frameId": request.FrameID, "toolId": request.ToolID, "alreadyPending": true,
		}, nil
	}
	if _, found, err := repository.GetActiveFrameTaskIntent(ctx, stream.UID, stream.OwnerID); err != nil {
		return nil, err
	} else if !found {
		frameEvent, err := store.AppendFrameEvent(workspace.FrameEventInput{
			ID: "e2e-ask-user-task-event:" + request.FrameID, FrameID: request.FrameID, Type: "user_message",
			Payload: map[string]any{
				"role": "user", "content": "Compare the candidate compounds and ask which option should continue.",
				"uuid": "e2e-ask-user-task-message:" + request.FrameID,
			},
		})
		if err != nil {
			return nil, err
		}
		if _, _, created, err := repository.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: "e2e-ask-user-task:" + request.FrameID, FrameEventID: frameEvent.ID,
			MessageUUID:   "e2e-ask-user-task-message:" + request.FrameID,
			MessageOrigin: "task_intent", Text: "Compare the candidate compounds and ask which option should continue.",
			Destinations: []string{"ws"},
		}); err != nil || !created {
			if err == nil {
				err = errors.New("ask-user fixture task intent was not created")
			}
			return nil, err
		}
	}
	claimed, err := repository.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "e2e-ask-user-fixture",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		if err == nil {
			err = errors.New("ask-user fixture runner claim was not acquired")
		}
		return nil, err
	}
	pausePayload, err := json.Marshal(map[string]any{
		"status": "awaiting_user_response", "detail": "waiting for the browser fixture response",
	})
	if err != nil {
		return nil, err
	}
	result, _, err := store.ParkAskUserWithTranscript(ctx, workspace.ParkAskUserInput{
		FrameID: request.FrameID, ToolID: request.ToolID, ToolName: "ask_user", Questions: questions,
	}, transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "e2e-ask-user-pause:" + request.ToolID,
		Phase: transcriptstore.RunnerPhaseWaitingUser, Resumable: true,
		PayloadJSON: pausePayload, Destinations: []string{"ws"},
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "frameId": request.FrameID, "toolId": request.ToolID, "alreadyPending": result.AlreadyPending,
	}, nil
}

func seedScrollHistoryFixture(store *workspace.Store, request fixtureRequest) (map[string]any, error) {
	ctx := context.Background()
	frameContext, found, err := store.GetFrameRealtimeContext(request.FrameID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("scroll-history frame realtime context is unavailable")
		}
		return nil, err
	}
	repository, err := store.TranscriptRepository(ctx)
	if err != nil {
		return nil, err
	}
	stream, found, err := repository.GetFrameStreamBySession(ctx, frameContext.UserID, request.FrameID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("scroll-history transcript stream is unavailable")
		}
		return nil, err
	}
	messageIDs := make([]string, 0, request.HistoryCount)
	for index := 0; index < request.HistoryCount; index++ {
		suffix := fmt.Sprintf("%03d", index)
		messageID := "e2e-scroll-message:" + request.FrameID + ":" + suffix
		_, _, _, err := repository.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: "e2e-scroll-client:" + request.FrameID + ":" + suffix,
			FrameEventID:    "e2e-scroll-frame:" + request.FrameID + ":" + suffix,
			MessageUUID:     messageID,
			Text:            "Durable scroll history " + suffix,
			Destinations:    []string{"ws"},
		})
		if err != nil {
			return nil, fmt.Errorf("append scroll history event %d: %w", index, err)
		}
		messageIDs = append(messageIDs, messageID)
	}
	return map[string]any{
		"ok": true, "frameId": request.FrameID, "messageIds": messageIDs,
	}, nil
}

func fixtureAskUserAlreadyPending(
	ctx context.Context,
	repository *transcriptstore.Repository,
	stream transcriptstore.Stream,
	toolID string,
	questions []any,
) (bool, error) {
	snapshot, err := repository.GetProjectionSnapshot(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		return false, err
	}
	expectedQuestions, err := json.Marshal(questions)
	if err != nil {
		return false, err
	}
	var prompt *transcriptstore.AskUserPromptV1
	var pending *transcriptstore.AskUserResultEventV1
	for after := int64(0); after < snapshot.ThroughPublicationSequence; {
		projected, listErr := repository.ListProjectedCoordinateEvents(ctx, transcriptstore.ListProjectedEventsInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
			AfterPublicationSequence: after, ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Limit: 256,
		})
		if listErr != nil {
			return false, listErr
		}
		if len(projected) == 0 {
			return false, transcriptstore.ErrEventConflict
		}
		for _, item := range projected {
			switch item.Event.Type {
			case transcriptstore.AskUserPromptEventType:
				decoded, decodeErr := transcriptstore.DecodeAskUserPromptV1(item.ResolvedPayloadJSON)
				if decodeErr != nil || decoded.ToolUseID != toolID {
					continue
				}
				if prompt != nil {
					return false, transcriptstore.ErrEventConflict
				}
				actualQuestions, marshalErr := json.Marshal(decoded.Questions)
				var normalizedQuestions any
				if marshalErr != nil || json.Unmarshal(actualQuestions, &normalizedQuestions) != nil {
					return false, transcriptstore.ErrEventConflict
				}
				actualQuestions, marshalErr = json.Marshal(normalizedQuestions)
				if marshalErr != nil || string(actualQuestions) != string(expectedQuestions) {
					return false, transcriptstore.ErrEventConflict
				}
				prompt = &decoded
			case transcriptstore.AskUserResultEventType:
				decoded, decodeErr := transcriptstore.DecodeAskUserResultEventV1(item.ResolvedPayloadJSON)
				if decodeErr != nil || decoded.ToolUseID != toolID {
					continue
				}
				if pending != nil || decoded.Result.Status != transcriptstore.AskUserStatusAwaitingResponse {
					return false, transcriptstore.ErrEventConflict
				}
				pending = &decoded
			}
		}
		next := projected[len(projected)-1].Event.PublicationSeq
		if next <= after {
			return false, transcriptstore.ErrEventConflict
		}
		after = next
	}
	if prompt == nil && pending == nil {
		return false, nil
	}
	if prompt == nil || pending == nil || prompt.Origin != pending.Origin {
		return false, transcriptstore.ErrEventConflict
	}
	return true, nil
}

func fixtureDatabasePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("SYNON_GO_E2E_WORKSPACE_DB is required")
	}
	if !filepath.IsAbs(value) {
		return "", errors.New("SYNON_GO_E2E_WORKSPACE_DB must be absolute")
	}
	path := filepath.Clean(value)
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect browser fixture workspace: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("browser fixture workspace must be a regular non-symlink file")
	}
	return path, nil
}
