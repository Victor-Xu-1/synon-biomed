package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type BindGeneratedPlanApprovalInput struct {
	FrameID, ToolCallID, RequestID             string
	ArtifactID, VersionID, Filename            string
	PlanSHA256, TaskIntentID, TaskIntentSHA256 string
	TaskIntentRevision                         int64
	PlanSizeBytes                              int64
	PlanJSON                                   map[string]any
}

// BindGeneratedPlanApprovalTransaction joins the exact plan artifact/version,
// active Frame status, and waiting-approval checkpoint transaction. The plan
// artifact is staged before this hook under a deterministic mutation key; no
// competing plan can become active for the same Frame.
func (s *Store) BindGeneratedPlanApprovalTransaction(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	input BindGeneratedPlanApprovalInput,
) (FrameEvent, error) {
	if s == nil || s.db == nil || tx == nil {
		return FrameEvent{}, errors.New("generated plan transaction authority is required")
	}
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.ToolCallID = strings.TrimSpace(input.ToolCallID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.ArtifactID = strings.TrimSpace(input.ArtifactID)
	input.VersionID = strings.TrimSpace(input.VersionID)
	input.Filename = strings.TrimSpace(input.Filename)
	input.PlanSHA256 = strings.TrimSpace(input.PlanSHA256)
	input.TaskIntentID = strings.TrimSpace(input.TaskIntentID)
	input.TaskIntentSHA256 = strings.TrimSpace(input.TaskIntentSHA256)
	if input.FrameID == "" || input.ToolCallID == "" || input.RequestID == "" ||
		input.ArtifactID == "" || input.VersionID == "" || input.Filename == "" ||
		input.PlanSHA256 == "" || input.PlanSizeBytes <= 0 || input.PlanJSON == nil {
		return FrameEvent{}, errors.New("complete generated plan approval identity is required")
	}
	ownerID, err := frameOwnerInTransaction(ctx, tx, input.FrameID)
	if err != nil {
		return FrameEvent{}, err
	}
	var status, projectID, rootFrameID, rawContext string
	if err := tx.QueryRowContext(ctx, `
		SELECT frame.status,frame.project_id,frame.root_frame_id,COALESCE(metadata.context_data,'{}')
		FROM frames frame LEFT JOIN frame_runtime_metadata metadata ON metadata.frame_id=frame.id
		WHERE frame.id=?`, input.FrameID,
	).Scan(&status, &projectID, &rootFrameID, &rawContext); err != nil {
		return FrameEvent{}, err
	}
	if status != FrameStatusProcessing && status != "running" {
		return FrameEvent{}, fmt.Errorf("generated plan requires a processing Frame, got %s", status)
	}
	var artifactProjectID, artifactName, contentSHA256 string
	var currentVersionNumber, versionNumber int64
	var sizeBytes int64
	if err := tx.QueryRowContext(ctx, `
		SELECT artifact.project_id,artifact.name,artifact.current_version_number,
			version.version_number,version.content_sha256,version.size_bytes
		FROM artifacts artifact JOIN artifact_versions version ON version.artifact_id=artifact.id
		WHERE artifact.id=? AND version.id=?`, input.ArtifactID, input.VersionID,
	).Scan(&artifactProjectID, &artifactName, &currentVersionNumber, &versionNumber, &contentSHA256, &sizeBytes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FrameEvent{}, errors.New("generated plan artifact version is unavailable")
		}
		return FrameEvent{}, err
	}
	if artifactProjectID != projectID || artifactName != input.Filename || currentVersionNumber != versionNumber ||
		contentSHA256 != input.PlanSHA256 || sizeBytes != input.PlanSizeBytes {
		return FrameEvent{}, transcriptstore.ErrEventConflict
	}
	var streamOwnerID, streamFrameID, streamProjectID, streamRootFrameID, streamKind string
	if err := tx.QueryRowContext(ctx, `
		SELECT owner_id,frame_id,project_id,root_frame_id,kind
		FROM transcript_streams WHERE session_id=?`, input.FrameID,
	).Scan(&streamOwnerID, &streamFrameID, &streamProjectID, &streamRootFrameID, &streamKind); err != nil {
		return FrameEvent{}, err
	}
	if streamOwnerID != ownerID || streamFrameID != input.FrameID || streamProjectID != projectID ||
		streamRootFrameID != rootFrameID || streamKind != "frame_ref" {
		return FrameEvent{}, transcriptstore.ErrEventConflict
	}
	contextData := map[string]any{}
	if err := json.Unmarshal([]byte(rawContext), &contextData); err != nil || contextData == nil {
		return FrameEvent{}, errors.New("generated plan Frame context is invalid")
	}
	if existing := strings.TrimSpace(compatibilityStringValue(contextData["_plan_artifact_id"])); existing != "" {
		return FrameEvent{}, errors.New("a plan decision is already pending for this Frame")
	}
	now := s.now().UTC()
	contextData["_plan_artifact_id"] = input.ArtifactID
	contextData["_plan_version_id"] = input.VersionID
	contextData["_plan_approval_base_version_id"] = input.VersionID
	contextData["_plan_generated_at"] = now.Format("2006-01-02T15:04:05.999999999Z07:00")
	contextData["_plan_file_path"] = input.Filename
	contextData["_plan_size_bytes"] = input.PlanSizeBytes
	contextData["_plan_sha256"] = input.PlanSHA256
	contextData["_plan_json"] = cloneCompatibilityMap(input.PlanJSON)
	contextData["_plan_schema_version"] = 3
	contextData["_plan_tool_call_id"] = input.ToolCallID
	contextData["_plan_request_id"] = input.RequestID
	contextData["_plan_task_intent_id"] = input.TaskIntentID
	contextData["_plan_task_intent_revision"] = input.TaskIntentRevision
	contextData["_plan_task_intent_sha256"] = input.TaskIntentSHA256
	contextData["_plan_approved"] = false
	contextData["_step_statuses"] = map[string]any{}
	for _, stale := range []string{
		"_plan_approved_at", "_plan_approval_id", "_plan_approval_fingerprint", "_plan_edited_sha256",
	} {
		delete(contextData, stale)
	}
	encodedContext, err := json.Marshal(contextData)
	if err != nil {
		return FrameEvent{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata(frame_id,context_data) VALUES(?,?)
		ON CONFLICT(frame_id) DO UPDATE SET context_data=excluded.context_data`,
		input.FrameID, string(encodedContext)); err != nil {
		return FrameEvent{}, err
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE frames SET status=?,updated_at=? WHERE id=? AND status IN ('processing','running')`,
		FrameStatusAwaitingPlanApproval, now, input.FrameID)
	if err != nil {
		return FrameEvent{}, err
	}
	if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return FrameEvent{}, err
		}
		return FrameEvent{}, transcriptstore.ErrEventConflict
	}
	event, err := appendFrameLifecycleEvent(ctx, tx, input.FrameID, "frame_plan_generated", map[string]any{
		"rootFrameId": rootFrameID, "status": FrameStatusAwaitingPlanApproval,
		"artifactId": input.ArtifactID, "versionId": input.VersionID,
		"toolCallId": input.ToolCallID, "requestId": input.RequestID,
	}, now)
	if err != nil {
		return FrameEvent{}, err
	}
	frame, err := frameForRealtimeInTransaction(ctx, tx, input.FrameID)
	if err != nil {
		return FrameEvent{}, err
	}
	if _, err := s.enqueueRealtimeOutboxTransaction(ctx, tx,
		FrameRealtimeEventInput("frame-event:"+event.ID, ownerID, frame, event), event.ID, "",
	); err != nil {
		return FrameEvent{}, err
	}
	return event, nil
}
