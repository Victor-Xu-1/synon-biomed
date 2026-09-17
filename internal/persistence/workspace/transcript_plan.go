package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
)

var compatibilityPlanContextKeys = []string{
	"_plan_approved", "_plan_approved_at", "_plan_artifact_id", "_plan_version_id",
	"_plan_approval_base_version_id", "_plan_approval_id", "_plan_approval_fingerprint", "_plan_edited_sha256",
	"_plan_generated_at", "_plan_pre_revision_version_id", "_plan_superseded_artifact_ids",
	"_plan_file_path", "_plan_size_bytes", "_plan_sha256", "_plan_json", "_plan_schema_version",
	"_plan_tool_call_id", "_plan_request_id", "_plan_task_intent_id", "_plan_task_intent_revision", "_plan_task_intent_sha256",
	"_plan_steps", "_plan_claims",
	"_plan_claimed_delegations", "_plan_step_denials", "_step_statuses",
}

var compatibilityPlanDiscardContextKeys = CompatibilityPlanContextKeys()

func CompatibilityPlanContextKeys() []string {
	return append([]string(nil), compatibilityPlanContextKeys...)
}

type ApproveCompatibilityPlanWithTranscriptInput struct {
	FrameID              string
	ApprovalID           string
	ApprovalFingerprint  string
	ClientMessageID      string
	Text                 string
	ExpectedPlanArtifact string
	ExpectedPlanVersion  string
	AgentName            string
	ContextData          map[string]any
	RuntimeConfig        map[string]any
	EditedPlanJSON       []byte
}

type CompatibilityPlanApprovalResult struct {
	Frame                CompatibilityFrame
	InputEvent           transcriptstore.Event
	ResumeEvent          FrameEvent
	Checkpoint           transcriptstore.RunnerCheckpoint
	Idempotent           bool
	EditedPlanVersionID  string
	BlobFinalizeDeferred bool
}

type CompatibilityPlanApprovalIdentityInput struct {
	FrameID, ArtifactID, BaseVersionID, Text, AgentName string
	RuntimeConfig                                       map[string]any
	EditedPlan                                          map[string]any
}

func BuildCompatibilityPlanApprovalIdentity(input CompatibilityPlanApprovalIdentityInput) (string, string, error) {
	type approvalIdentity struct {
		Version       int            `json:"version"`
		FrameID       string         `json:"frame_id"`
		ArtifactID    string         `json:"artifact_id"`
		BaseVersionID string         `json:"base_version_id,omitempty"`
		Text          string         `json:"text"`
		AgentName     string         `json:"agent_name,omitempty"`
		EditedPlan    map[string]any `json:"edited_plan,omitempty"`
		RuntimeConfig map[string]any `json:"runtime_config"`
	}
	identity := approvalIdentity{
		Version: 1, FrameID: strings.TrimSpace(input.FrameID), ArtifactID: strings.TrimSpace(input.ArtifactID),
		BaseVersionID: strings.TrimSpace(input.BaseVersionID), Text: strings.TrimSpace(input.Text),
		AgentName: strings.TrimSpace(input.AgentName), EditedPlan: cloneCompatibilityMap(input.EditedPlan),
		RuntimeConfig: cloneCompatibilityMap(input.RuntimeConfig),
	}
	if identity.FrameID == "" || identity.ArtifactID == "" || identity.Text == "" || identity.RuntimeConfig == nil {
		return "", "", errors.New("complete plan approval identity is required")
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(digest[:])
	return "plan-approval:v1:" + fingerprint, fingerprint, nil
}

type compatibilityEditedPlanStage struct {
	VersionID, TemporaryPath, StagingPath, FinalPath, SHA256 string
	SizeBytes                                                int64
}

// ApproveCompatibilityPlanWithTranscript atomically binds the approved plan,
// exact input response, waiting-approval checkpoint, Frame transition, and
// dedicated resume dispatch. No intermediate "processing without work" state
// can escape the SQLite transaction.
func (s *Store) ApproveCompatibilityPlanWithTranscript(
	ctx context.Context,
	input ApproveCompatibilityPlanWithTranscriptInput,
) (CompatibilityPlanApprovalResult, error) {
	if s == nil || s.db == nil {
		return CompatibilityPlanApprovalResult{}, errors.New("workspace store is closed")
	}
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.ApprovalID = strings.TrimSpace(input.ApprovalID)
	input.ApprovalFingerprint = strings.TrimSpace(input.ApprovalFingerprint)
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	input.Text = strings.TrimSpace(input.Text)
	input.ExpectedPlanArtifact = strings.TrimSpace(input.ExpectedPlanArtifact)
	input.ExpectedPlanVersion = strings.TrimSpace(input.ExpectedPlanVersion)
	input.AgentName = strings.TrimSpace(input.AgentName)
	if input.FrameID == "" || input.ApprovalID == "" || input.ApprovalFingerprint == "" || input.ClientMessageID != input.ApprovalID || input.Text == "" ||
		input.ExpectedPlanArtifact == "" || input.RuntimeConfig == nil {
		return CompatibilityPlanApprovalResult{}, errors.New("complete plan approval authority is required")
	}
	var editedPlan map[string]any
	if len(input.EditedPlanJSON) > 0 {
		if err := json.Unmarshal(input.EditedPlanJSON, &editedPlan); err != nil || editedPlan == nil {
			return CompatibilityPlanApprovalResult{}, errors.New("edited plan must be a JSON object")
		}
		canonical, err := json.Marshal(editedPlan)
		if err != nil {
			return CompatibilityPlanApprovalResult{}, err
		}
		input.EditedPlanJSON = canonical
	}
	expectedApprovalID, expectedFingerprint, err := BuildCompatibilityPlanApprovalIdentity(CompatibilityPlanApprovalIdentityInput{
		FrameID: input.FrameID, ArtifactID: input.ExpectedPlanArtifact, BaseVersionID: input.ExpectedPlanVersion,
		Text: input.Text, AgentName: input.AgentName, RuntimeConfig: input.RuntimeConfig, EditedPlan: editedPlan,
	})
	if err != nil {
		return CompatibilityPlanApprovalResult{}, err
	}
	if input.ApprovalID != expectedApprovalID || input.ApprovalFingerprint != expectedFingerprint {
		return CompatibilityPlanApprovalResult{}, transcriptstore.ErrEventConflict
	}
	var editedStage *compatibilityEditedPlanStage
	if len(input.EditedPlanJSON) > 0 {
		if input.ExpectedPlanVersion == "" {
			return CompatibilityPlanApprovalResult{}, errors.New("edited plan approval requires an expected plan version")
		}
		var err error
		editedStage, err = s.stageCompatibilityEditedPlan(ctx, input.ApprovalFingerprint, input.EditedPlanJSON)
		if err != nil {
			return CompatibilityPlanApprovalResult{}, err
		}
	}
	stageCommitted := false
	defer func() {
		if editedStage != nil && !stageCommitted {
			_ = os.Remove(editedStage.TemporaryPath)
		}
	}()
	repository := transcriptstore.NewRepository(s.db)
	var result CompatibilityPlanApprovalResult
	var editedMarker blobCommitMarker
	outcome, err := repository.RunImmediateWithOutcome(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		ownerID, err := frameOwnerInTransaction(ctx, tx, input.FrameID)
		if err != nil {
			return err
		}
		var status, projectID, rootFrameID, storedAgent, rawContext string
		if err := tx.QueryRowContext(ctx, `
			SELECT frame.status,frame.project_id,frame.root_frame_id,frame.agent_name,COALESCE(metadata.context_data,'{}')
			FROM frames frame LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id=frame.id
			WHERE frame.id=?`, input.FrameID,
		).Scan(&status, &projectID, &rootFrameID, &storedAgent, &rawContext); err != nil {
			return err
		}
		storedContext := map[string]any{}
		if json.Unmarshal([]byte(rawContext), &storedContext) != nil {
			return transcriptstore.ErrEventConflict
		}
		if compatibilityPlanContextBool(storedContext["_plan_approved"]) {
			if strings.TrimSpace(fmt.Sprint(storedContext["_plan_approval_id"])) == input.ApprovalID &&
				strings.TrimSpace(fmt.Sprint(storedContext["_plan_approval_fingerprint"])) == input.ApprovalFingerprint {
				storedEditedSHA := strings.TrimSpace(fmt.Sprint(storedContext["_plan_edited_sha256"]))
				if editedStage == nil && storedEditedSHA != "" && storedEditedSHA != "<nil>" ||
					editedStage != nil && storedEditedSHA != editedStage.SHA256 {
					return transcriptstore.ErrEventConflict
				}
				result.Idempotent = true
				return nil
			}
			return transcriptstore.ErrEventConflict
		}
		if status != FrameStatusAwaitingPlanApproval {
			return fmt.Errorf("frame is not awaiting plan approval: %s", status)
		}
		if strings.TrimSpace(fmt.Sprint(storedContext["_plan_artifact_id"])) != input.ExpectedPlanArtifact ||
			(input.ExpectedPlanVersion != "" && strings.TrimSpace(fmt.Sprint(storedContext["_plan_version_id"])) != input.ExpectedPlanVersion) ||
			compatibilityPlanContextBool(storedContext["_plan_approved"]) {
			return transcriptstore.ErrEventConflict
		}
		authority, found, err := tx.GetFrameAuthorityBySession(ctx, ownerID, input.FrameID)
		if err != nil {
			return err
		}
		if !found || !authority.TranscriptPayloadActive() {
			return transcriptstore.ErrEventConflict
		}
		checkpoint, err := tx.ReleaseLatestFrameRunnerForResume(
			ctx, authority.ActiveStreamUID, ownerID, transcriptstore.RunnerPhaseWaitingApproval,
		)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		contextData := cloneCompatibilityMap(storedContext)
		if editedStage != nil {
			version, marker, err := s.commitCompatibilityEditedPlan(
				ctx, tx, *editedStage, input.ExpectedPlanArtifact, input.ExpectedPlanVersion,
				projectID, rootFrameID, input.FrameID, now,
			)
			if err != nil {
				return err
			}
			contextData["_plan_version_id"] = version.ID
			contextData["_plan_edited_sha256"] = editedStage.SHA256
			contextData["_plan_json"] = cloneCompatibilityMap(editedPlan)
			contextData["_step_statuses"] = map[string]any{}
			contextData["_plan_step_denials"] = float64(0)
			contextData["_plan_steps"] = nil
			result.EditedPlanVersionID = version.ID
			editedMarker = marker
		}
		contextData["_plan_approved"] = true
		contextData["_plan_approved_at"] = now.Format(time.RFC3339Nano)
		contextData["_plan_approval_id"] = input.ApprovalID
		contextData["_plan_approval_fingerprint"] = input.ApprovalFingerprint
		contextData["_plan_approval_base_version_id"] = input.ExpectedPlanVersion
		encodedContext, err := json.Marshal(contextData)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO frame_runtime_metadata(frame_id,context_data) VALUES(?,?)
			ON CONFLICT(frame_id) DO UPDATE SET context_data=excluded.context_data`,
			input.FrameID, string(encodedContext)); err != nil {
			return err
		}
		messagePayload := map[string]any{
			"messageUuid": input.ClientMessageID, "clientMessageId": input.ClientMessageID,
			"text": input.Text, "role": "user", "_uuid": input.ClientMessageID,
			"messageOrigin": "input_response", "planApprovalId": input.ApprovalID,
			"runtimeConfig": cloneCompatibilityMap(input.RuntimeConfig),
			"content":       []any{map[string]any{"type": "text", "text": input.Text}},
		}
		encodedMessage, err := json.Marshal(messagePayload)
		if err != nil {
			return err
		}
		inputEvent, created, err := tx.AppendFrameInputResponse(ctx, transcriptstore.AppendFrameInputResponseInput{
			StreamUID: authority.ActiveStreamUID, OwnerID: ownerID, FrameID: input.FrameID,
			ClientMessageID: input.ClientMessageID, PayloadJSON: encodedMessage, Destinations: []string{"ws"},
		})
		if err != nil || !created {
			if err != nil {
				return err
			}
			return transcriptstore.ErrEventConflict
		}
		agentName := input.AgentName
		if agentName == "" {
			agentName = storedAgent
		}
		updated, err := tx.ExecContext(ctx, `
			UPDATE frames SET status='processing',agent_name=?,updated_at=?
			WHERE id=? AND status='awaiting_plan_approval'`, agentName, now, input.FrameID)
		if err != nil {
			return err
		}
		if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return transcriptstore.ErrEventConflict
		}
		resumeEvent, err := appendFrameLifecycleEvent(ctx, tx, input.FrameID, "frame_resumed", map[string]any{
			"previousStatus": "awaiting_plan_approval", "rootFrameId": rootFrameID,
			"agentName": agentName, "reason": "plan_approved", "approvalId": input.ApprovalID,
			"clientMessageId": input.ClientMessageID, "requestHash": input.ApprovalFingerprint,
			"inputEventId": inputEvent.EventID, "runnerAttempt": checkpoint.Attempt,
			"checkpointSequence": checkpoint.Sequence, "controls": cloneCompatibilityMap(input.RuntimeConfig),
			"dispatch": map[string]any{
				"status": frameResumeDispatchRegistered, "attempt": 0,
				"registeredAt": now.Format(time.RFC3339Nano),
			},
		}, now)
		if err != nil {
			return err
		}
		frameProjection := Frame{
			ID: input.FrameID, ProjectID: projectID, RootFrameID: rootFrameID,
			AgentName: agentName, Status: FrameStatusProcessing,
		}
		if _, err := s.enqueueRealtimeOutboxTransaction(ctx, tx,
			FrameRealtimeEventInput("frame-event:"+resumeEvent.ID, ownerID, frameProjection, resumeEvent),
			resumeEvent.ID, "",
		); err != nil {
			return err
		}
		result.InputEvent = inputEvent
		result.ResumeEvent = resumeEvent
		result.Checkpoint = checkpoint
		result.Frame.ID = input.FrameID
		result.Frame.RootFrameID = rootFrameID
		result.Frame.Status = FrameStatusProcessing
		result.Frame.AgentName = agentName
		return nil
	})
	if err != nil {
		if outcome.CommitAttempted {
			if editedStage != nil {
				// COMMIT may have succeeded even when the driver reported an
				// error. Preserve the only staged blob until a durable receipt
				// proves rollback.
				stageCommitted = true
			}
			committed, resolveErr := s.compatibilityPlanApprovalCommitted(
				context.WithoutCancel(ctx), input, editedStage,
			)
			if resolveErr != nil {
				return CompatibilityPlanApprovalResult{}, errors.Join(err, fmt.Errorf("resolve plan approval commit outcome: %w", resolveErr))
			}
			if committed {
				err = nil
			} else {
				stageCommitted = false
			}
		}
		if err != nil {
			return CompatibilityPlanApprovalResult{}, err
		}
	}
	if editedStage != nil && !result.Idempotent {
		stageCommitted = true
		finalizeCtx := context.WithoutCancel(ctx)
		if err := s.finalizeBlobCommit(finalizeCtx, editedMarker); err != nil {
			if recoverErr := s.recoverBlobCommits(finalizeCtx); recoverErr != nil {
				result.BlobFinalizeDeferred = true
			}
		}
	}
	if result.Idempotent {
		if err := s.loadCompatibilityPlanApprovalReceipt(context.WithoutCancel(ctx), input, &result); err != nil {
			return CompatibilityPlanApprovalResult{}, err
		}
		if editedStage != nil {
			if err := s.recoverBlobCommits(context.WithoutCancel(ctx)); err != nil {
				result.BlobFinalizeDeferred = true
			}
		}
	}
	frame, found, reloadErr := s.GetCompatibilityFrame(input.FrameID)
	if reloadErr == nil && found {
		result.Frame = frame
	}
	s.signalOutboxWake()
	s.signalKernelRetentionWake()
	return result, nil
}

func (s *Store) loadCompatibilityPlanApprovalReceipt(
	ctx context.Context,
	input ApproveCompatibilityPlanWithTranscriptInput,
	result *CompatibilityPlanApprovalResult,
) error {
	if result == nil {
		return errors.New("plan approval receipt target is required")
	}
	frame, found, err := s.GetCompatibilityFrame(input.FrameID)
	if err != nil || !found {
		return errors.Join(err, errors.New("approved plan frame is unavailable"))
	}
	dispatch, found, err := s.GetCompatibilityFrameResumeDispatchByFrame(input.FrameID)
	if err != nil || !found {
		return errors.Join(err, errors.New("approved plan resume dispatch is unavailable"))
	}
	if strings.TrimSpace(stringValue(dispatch.ResumeEvent.Payload["approvalId"])) != input.ApprovalID ||
		strings.TrimSpace(stringValue(dispatch.ResumeEvent.Payload["requestHash"])) != input.ApprovalFingerprint {
		return transcriptstore.ErrEventConflict
	}
	attempt, validAttempt := exactWorkspacePositiveInt64(dispatch.ResumeEvent.Payload["runnerAttempt"])
	sequence, validSequence := exactWorkspacePositiveInt64(dispatch.ResumeEvent.Payload["checkpointSequence"])
	if !validAttempt || !validSequence {
		return transcriptstore.ErrCheckpointUnavailable
	}
	frameContext, found, err := s.GetFrameRealtimeContext(input.FrameID)
	if err != nil || !found {
		return errors.Join(err, errors.New("approved plan frame context is unavailable"))
	}
	repository := transcriptstore.NewRepository(s.db)
	stream, found, err := repository.GetFrameStreamBySession(ctx, frameContext.UserID, input.FrameID)
	if err != nil || !found {
		return errors.Join(err, transcriptstore.ErrSchemaUnavailable)
	}
	checkpoint, found, err := repository.GetResumableCheckpoint(ctx, stream.UID, frameContext.UserID, attempt, sequence)
	if err != nil || !found || checkpoint.Phase != transcriptstore.RunnerPhaseWaitingApproval {
		return errors.Join(err, transcriptstore.ErrCheckpointUnavailable)
	}
	inputEvent, found, err := repository.GetEventByClientMessageID(
		ctx, stream.UID, frameContext.UserID, input.ClientMessageID,
	)
	if err != nil || !found || inputEvent.Type != "user_input_response" {
		return errors.Join(err, transcriptstore.ErrEventConflict)
	}
	metadata, found, err := s.GetFrameRuntimeMetadata(input.FrameID)
	if err != nil || !found {
		return errors.Join(err, errors.New("approved plan metadata is unavailable"))
	}
	result.Frame = frame
	result.InputEvent = inputEvent
	result.ResumeEvent = dispatch.ResumeEvent
	result.Checkpoint = checkpoint
	result.EditedPlanVersionID = strings.TrimSpace(stringValue(metadata.ContextData["_plan_version_id"]))
	return nil
}

func exactWorkspacePositiveInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), typed > 0
	case int64:
		return typed, typed > 0
	case float64:
		converted := int64(typed)
		return converted, typed > 0 && float64(converted) == typed
	case json.Number:
		converted, err := typed.Int64()
		return converted, err == nil && converted > 0
	default:
		return 0, false
	}
}

func (s *Store) compatibilityPlanApprovalCommitted(
	ctx context.Context,
	input ApproveCompatibilityPlanWithTranscriptInput,
	editedStage *compatibilityEditedPlanStage,
) (bool, error) {
	metadata, found, err := s.GetFrameRuntimeMetadata(input.FrameID)
	if err != nil || !found {
		return false, err
	}
	contextData := metadata.ContextData
	if !compatibilityPlanContextBool(contextData["_plan_approved"]) {
		return false, nil
	}
	if strings.TrimSpace(fmt.Sprint(contextData["_plan_approval_id"])) != input.ApprovalID ||
		strings.TrimSpace(fmt.Sprint(contextData["_plan_approval_fingerprint"])) != input.ApprovalFingerprint ||
		strings.TrimSpace(fmt.Sprint(contextData["_plan_approval_base_version_id"])) != input.ExpectedPlanVersion {
		return false, transcriptstore.ErrEventConflict
	}
	if editedStage != nil {
		if strings.TrimSpace(fmt.Sprint(contextData["_plan_edited_sha256"])) != editedStage.SHA256 ||
			strings.TrimSpace(fmt.Sprint(contextData["_plan_version_id"])) != editedStage.VersionID {
			return false, transcriptstore.ErrEventConflict
		}
	}
	dispatch, found, err := s.GetCompatibilityFrameResumeDispatchByFrame(input.FrameID)
	if err != nil || !found {
		return false, err
	}
	if strings.TrimSpace(stringValue(dispatch.ResumeEvent.Payload["approvalId"])) != input.ApprovalID ||
		strings.TrimSpace(stringValue(dispatch.ResumeEvent.Payload["requestHash"])) != input.ApprovalFingerprint ||
		strings.TrimSpace(stringValue(dispatch.ResumeEvent.Payload["clientMessageId"])) != input.ClientMessageID {
		return false, transcriptstore.ErrEventConflict
	}
	return true, nil
}

func (s *Store) stageCompatibilityEditedPlan(
	ctx context.Context,
	approvalFingerprint string,
	content []byte,
) (*compatibilityEditedPlanStage, error) {
	versionID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("synon-plan-edit:v1:"+approvalFingerprint)).String()
	temporary, size, digest, err := s.stageArtifactWrite(ctx, bytes.NewReader(content), 4<<20)
	if err != nil {
		return nil, err
	}
	stagingPath, err := s.blobRelative(temporary)
	if err != nil {
		_ = os.Remove(temporary)
		return nil, err
	}
	return &compatibilityEditedPlanStage{
		VersionID: versionID, TemporaryPath: temporary, StagingPath: stagingPath,
		FinalPath: artifactVersionBlobPath(versionID), SHA256: digest, SizeBytes: size,
	}, nil
}

func (s *Store) commitCompatibilityEditedPlan(
	ctx context.Context,
	tx workspaceTransaction,
	stage compatibilityEditedPlanStage,
	artifactID, expectedVersionID, projectID, rootFrameID, frameID string,
	now time.Time,
) (ArtifactVersion, blobCommitMarker, error) {
	var name, currentVersionID string
	var currentVersion int
	if err := tx.QueryRowContext(ctx, `
		SELECT artifact.name,artifact.current_version_number,version.id
		FROM artifacts artifact JOIN artifact_versions version
			ON version.artifact_id=artifact.id AND version.version_number=artifact.current_version_number
		WHERE artifact.id=? AND artifact.project_id=?`, artifactID, projectID,
	).Scan(&name, &currentVersion, &currentVersionID); err != nil {
		return ArtifactVersion{}, blobCommitMarker{}, fmt.Errorf("look up approved plan artifact: %w", err)
	}
	if currentVersionID != expectedVersionID {
		return ArtifactVersion{}, blobCommitMarker{}, transcriptstore.ErrEventConflict
	}
	lowerName := strings.ToLower(strings.TrimSpace(name))
	if lowerName != "plan.json" && !(strings.HasPrefix(lowerName, "plan_") && strings.HasSuffix(lowerName, ".json")) {
		return ArtifactVersion{}, blobCommitMarker{}, fmt.Errorf("edited plan artifact has unsupported name %q", name)
	}
	version := ArtifactVersion{
		ID: stage.VersionID, ArtifactID: artifactID, VersionNumber: currentVersion + 1,
		ParentID: expectedVersionID, ContentSHA256: stage.SHA256, StoragePath: stage.FinalPath,
		SizeBytes: stage.SizeBytes, CreatedBy: "user", CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions
			(id,artifact_id,version_number,parent_id,content,content_sha256,storage_path,size_bytes,created_by,created_at)
		VALUES(?,?,?,?,X'',?,?,?,?,?)`,
		version.ID, version.ArtifactID, version.VersionNumber, version.ParentID,
		version.ContentSHA256, version.StoragePath, version.SizeBytes, version.CreatedBy, version.CreatedAt,
	); err != nil {
		return ArtifactVersion{}, blobCommitMarker{}, fmt.Errorf("insert edited plan version: %w", err)
	}
	writeInput := WriteArtifactVersionInput{
		ArtifactID: artifactID, ProjectID: projectID, Name: name, ContentType: "application/json",
		CreatedBy: "user", ParentVersionID: expectedVersionID, ProvenanceSourceID: expectedVersionID,
		FreshUserEditMappings: true, RootFrameID: rootFrameID, FrameID: frameID,
	}
	if err := copyArtifactWriteProvenance(ctx, tx, writeInput, version); err != nil {
		return ArtifactVersion{}, blobCommitMarker{}, err
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE artifacts SET kind='application/json',current_version_number=?,updated_at=?
		WHERE id=? AND project_id=? AND current_version_number=?`,
		version.VersionNumber, now, artifactID, projectID, currentVersion,
	)
	if err != nil {
		return ArtifactVersion{}, blobCommitMarker{}, fmt.Errorf("advance edited plan version: %w", err)
	}
	if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return ArtifactVersion{}, blobCommitMarker{}, err
		}
		return ArtifactVersion{}, blobCommitMarker{}, transcriptstore.ErrEventConflict
	}
	if err := updateArtifactWriteRuntimeMetadata(ctx, tx, writeInput, version); err != nil {
		return ArtifactVersion{}, blobCommitMarker{}, err
	}
	marker := blobCommitMarker{
		ID: "artifact-version:" + version.ID, Kind: "artifact_version", AggregateID: version.ID,
		StagingPath: stage.StagingPath, FinalPath: stage.FinalPath, SHA256: stage.SHA256,
		SizeBytes: stage.SizeBytes, CreatedAt: now,
	}
	if err := s.insertBlobCommitMarkerTx(ctx, tx, marker); err != nil {
		return ArtifactVersion{}, blobCommitMarker{}, err
	}
	return version, marker, nil
}

func compatibilityPlanContextBool(value any) bool {
	result, _ := value.(bool)
	return result
}

// DiscardCompatibilityPlanWithTranscript settles the paused runner, Frame,
// metadata, and lifecycle event in the workspace SQLite transaction.
func (s *Store) DiscardCompatibilityPlanWithTranscript(
	ctx context.Context,
	frameID, clientMessageID string,
) (CompatibilityFrame, FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return CompatibilityFrame{}, FrameEvent{}, false, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	clientMessageID = strings.TrimSpace(clientMessageID)
	if frameID == "" {
		return CompatibilityFrame{}, FrameEvent{}, false, errors.New("frame id is required")
	}
	repository := transcriptstore.NewRepository(s.db)
	var event FrameEvent
	var idempotent bool
	err := repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		ownerID, err := frameOwnerInTransaction(ctx, tx, frameID)
		if err != nil {
			return err
		}
		if clientMessageID != "" {
			event, idempotent, err = existingFrameControlEvent(ctx, tx, frameID, "plan_discarded", clientMessageID)
			if err != nil {
				return err
			}
			if idempotent {
				_, _, _, err = tx.CompleteLatestFrameRunner(ctx, ownerID, frameID, "plan_discarded", []string{"ws"})
				return err
			}
		}
		var status, rawContext string
		if err := tx.QueryRowContext(ctx, `
			SELECT frame.status,COALESCE(metadata.context_data,'{}')
			FROM frames frame LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id=frame.id
			WHERE frame.id=?`, frameID).Scan(&status, &rawContext); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("frame %q does not exist", frameID)
			}
			return err
		}
		if status != FrameStatusAwaitingPlanApproval {
			return fmt.Errorf("frame is not awaiting plan approval: %s", status)
		}
		contextData := map[string]any{}
		if err := json.Unmarshal([]byte(rawContext), &contextData); err != nil {
			return fmt.Errorf("decode plan metadata: %w", err)
		}
		artifactID, _ := contextData["_plan_artifact_id"].(string)
		for _, key := range compatibilityPlanDiscardContextKeys {
			delete(contextData, key)
		}
		updatedContext, err := json.Marshal(contextData)
		if err != nil {
			return fmt.Errorf("encode discarded plan metadata: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO frame_runtime_metadata(frame_id,context_data) VALUES(?,?)
			ON CONFLICT(frame_id) DO UPDATE SET context_data=excluded.context_data`,
			frameID, string(updatedContext)); err != nil {
			return fmt.Errorf("clear discarded plan metadata: %w", err)
		}
		if _, _, _, err := tx.CompleteLatestFrameRunner(
			ctx, ownerID, frameID, "plan_discarded", []string{"ws"},
		); err != nil {
			return err
		}
		payload := map[string]any{"reason": "plan_discarded"}
		if clientMessageID != "" {
			payload["clientMessageId"] = clientMessageID
		}
		if artifactID = strings.TrimSpace(artifactID); artifactID != "" {
			payload["artifact_id"] = artifactID
		}
		event, err = appendFrameLifecycleEvent(ctx, tx, frameID, "plan_discarded", payload, s.now().UTC())
		return err
	})
	if err != nil {
		return CompatibilityFrame{}, FrameEvent{}, false, err
	}
	frame, found, err := s.GetCompatibilityFrame(frameID)
	if err != nil {
		return CompatibilityFrame{}, FrameEvent{}, false, err
	}
	if !found {
		return CompatibilityFrame{}, FrameEvent{}, false, errors.New("discarded plan frame disappeared")
	}
	s.signalKernelRetentionWake()
	return frame, event, idempotent, nil
}
