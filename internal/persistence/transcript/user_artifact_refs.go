package transcript

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type ResolveLiveFrameUserArtifactInput struct {
	Claim             RunnerClaim
	ToolSourceEventID int64
	ToolName          string
	MessageContext    string
	RuntimeModeKey    string
	RuntimeModeValue  string
	VersionID         string
}

type userArtifactReferenceQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func normalizeUserArtifactReferenceInputs(values []UserArtifactReferenceInput) ([]UserArtifactReferenceInput, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make([]UserArtifactReferenceInput, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value.ArtifactID = strings.TrimSpace(value.ArtifactID)
		value.VersionID = strings.TrimSpace(value.VersionID)
		if value.ArtifactID == "" || value.VersionID == "" {
			return nil, errors.New("artifact id and version id are required")
		}
		key := value.ArtifactID + "\x00" + value.VersionID
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.New("artifact references must be unique")
		}
		seen[key] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func validateUserArtifactReferences(
	ctx context.Context,
	queryer userArtifactReferenceQueryer,
	stream Stream,
	values []UserArtifactReferenceInput,
) ([]UserArtifactReference, error) {
	normalized, err := normalizeUserArtifactReferenceInputs(values)
	if err != nil {
		return nil, err
	}
	result := make([]UserArtifactReference, 0, len(normalized))
	for _, value := range normalized {
		var ref UserArtifactReference
		err := queryer.QueryRowContext(ctx, `SELECT artifact.id,version.id,artifact.name,artifact.kind,
			CASE WHEN COALESCE(version.storage_path,'')='' THEN length(version.content) ELSE version.size_bytes END,
			version.content_sha256
			FROM artifact_versions version
			JOIN artifacts artifact ON artifact.id=version.artifact_id
			JOIN projects project ON project.id=artifact.project_id
			WHERE project.user_id=? AND project.id=? AND artifact.id=? AND version.id=?`,
			stream.OwnerID, stream.ProjectID, value.ArtifactID, value.VersionID,
		).Scan(&ref.ArtifactID, &ref.VersionID, &ref.Filename, &ref.ContentType, &ref.SizeBytes, &ref.Checksum)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrArtifactMissing
		}
		if err != nil {
			return nil, err
		}
		result = append(result, ref)
	}
	return result, nil
}

func userArtifactReferenceInputsMatch(
	inputs []UserArtifactReferenceInput,
	persisted []UserArtifactReference,
) bool {
	if len(inputs) != len(persisted) {
		return false
	}
	for index := range inputs {
		if inputs[index].ArtifactID != strings.TrimSpace(persisted[index].ArtifactID) ||
			inputs[index].VersionID != strings.TrimSpace(persisted[index].VersionID) {
			return false
		}
	}
	return true
}

func (r *Repository) ValidateUserArtifactReferences(
	ctx context.Context,
	streamUID, ownerID string,
	values []UserArtifactReferenceInput,
) ([]UserArtifactReference, error) {
	if r == nil || r.db == nil {
		return nil, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" {
		return nil, errors.New("stream and owner are required")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, schemaError(err)
	}
	defer conn.Close()
	stream, err := getStreamConn(ctx, conn, streamUID, ownerID)
	if err != nil {
		return nil, schemaError(err)
	}
	refs, err := validateUserArtifactReferences(ctx, conn, stream, values)
	return refs, schemaError(err)
}

// ResolveLiveFrameUserArtifactReference binds a read to the active task input,
// the exact tool-start checkpoint, and a current live runner claim in one
// BEGIN IMMEDIATE transaction. It never authorizes a same-project artifact
// that was not attached to the active task.
func (r *Repository) ResolveLiveFrameUserArtifactReference(
	ctx context.Context,
	input ResolveLiveFrameUserArtifactInput,
) (Stream, UserArtifactReference, error) {
	if r == nil || r.db == nil {
		return Stream{}, UserArtifactReference{}, ErrSchemaUnavailable
	}
	input.ToolName = strings.TrimSpace(input.ToolName)
	input.MessageContext = strings.TrimSpace(input.MessageContext)
	input.RuntimeModeKey = strings.TrimSpace(input.RuntimeModeKey)
	input.RuntimeModeValue = strings.TrimSpace(input.RuntimeModeValue)
	input.VersionID = strings.TrimSpace(input.VersionID)
	if input.ToolSourceEventID <= 0 || input.ToolName == "" || input.MessageContext == "" ||
		input.RuntimeModeKey == "" || input.RuntimeModeValue == "" || input.VersionID == "" {
		return Stream{}, UserArtifactReference{}, errors.New("live artifact reference input is invalid")
	}
	var stream Stream
	var reference UserArtifactReference
	err := r.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		var err error
		stream, err = tx.ValidateLiveRunnerClaim(ctx, input.Claim)
		if err != nil {
			return err
		}
		if stream.Kind != StreamKindFrameRef || stream.SessionID == "" || stream.FrameID == "" ||
			stream.SessionID != stream.FrameID {
			return ErrEventConflict
		}
		if err := validateLiveArtifactToolSource(ctx, tx.conn, stream, input); err != nil {
			return err
		}
		authority, found, err := getFrameAuthorityBySessionQuery(ctx, tx.conn, stream.OwnerID, stream.SessionID)
		if err != nil {
			return err
		}
		if !found {
			return ErrEventConflict
		}
		if !authority.TranscriptPayloadActive() || authority.ActiveStreamUID != stream.UID || authority.ActiveEpoch != stream.Epoch {
			return ErrEventConflict
		}
		intent, found, err := latestPayloadFrameTaskIntent(ctx, tx.conn, stream)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return ErrEventConflict
		}
		event, found, err := findEventByClientID(ctx, tx.conn, stream.UID, intent.SourceEventID)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return ErrEventConflict
		}
		if event.Type != "user_message" || event.Source != EventSourcePayload || event.RunnerAttempt != nil || event.FrameEventID != nil {
			return ErrEventConflict
		}
		refs, err := decodeLiveFrameUserArtifactPayload(event.PayloadJSON, input)
		if err != nil {
			return err
		}
		canonical, err := validateUserArtifactReferences(ctx, tx.conn, stream, refs)
		if err != nil {
			return err
		}
		for _, candidate := range canonical {
			if candidate.VersionID == input.VersionID {
				reference = candidate
				return nil
			}
		}
		return ErrArtifactMissing
	})
	return stream, reference, schemaError(err)
}

func validateLiveArtifactToolSource(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	input ResolveLiveFrameUserArtifactInput,
) error {
	var eventType string
	var runnerAttempt sql.NullInt64
	var payloadJSON []byte
	err := conn.QueryRowContext(ctx, `SELECT event_type,runner_attempt,payload_json FROM transcript_events
		WHERE stream_uid=? AND event_id=?`, stream.UID, input.ToolSourceEventID).
		Scan(&eventType, &runnerAttempt, &payloadJSON)
	if errors.Is(err, sql.ErrNoRows) || err == nil &&
		(eventType != "runner_checkpoint" || !runnerAttempt.Valid || runnerAttempt.Int64 != input.Claim.Attempt) {
		return ErrClaimStale
	}
	if err != nil {
		return err
	}
	var source struct {
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		ToolPhase  string          `json:"toolPhase"`
		ToolInput  json.RawMessage `json:"toolInput"`
	}
	if json.Unmarshal(payloadJSON, &source) != nil || strings.TrimSpace(source.ToolCallID) == "" ||
		strings.TrimSpace(source.ToolName) != input.ToolName || strings.TrimSpace(source.ToolPhase) != "start" {
		return ErrEventConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(source.ToolInput))
	var toolInput struct {
		VersionID string `json:"version_id"`
	}
	if decoder.Decode(&toolInput) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		strings.TrimSpace(toolInput.VersionID) != input.VersionID {
		return ErrEventConflict
	}
	return nil
}

func decodeLiveFrameUserArtifactPayload(
	payloadJSON []byte,
	input ResolveLiveFrameUserArtifactInput,
) ([]UserArtifactReferenceInput, error) {
	var payload map[string]json.RawMessage
	if len(payloadJSON) == 0 || json.Unmarshal(payloadJSON, &payload) != nil {
		return nil, ErrEventConflict
	}
	var messageContext string
	if json.Unmarshal(payload["messageContext"], &messageContext) != nil || strings.TrimSpace(messageContext) != input.MessageContext {
		return nil, ErrEventConflict
	}
	var runtimeConfig map[string]any
	if json.Unmarshal(payload["runtimeConfig"], &runtimeConfig) != nil ||
		strings.TrimSpace(stringValueFromAny(runtimeConfig[input.RuntimeModeKey])) != input.RuntimeModeValue {
		return nil, ErrEventConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(payload["artifactRefs"]))
	decoder.DisallowUnknownFields()
	var persisted []UserArtifactReference
	if decoder.Decode(&persisted) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(persisted) == 0 {
		return nil, ErrEventConflict
	}
	refs := make([]UserArtifactReferenceInput, 0, len(persisted))
	for _, candidate := range persisted {
		if strings.TrimSpace(candidate.ArtifactID) == "" || strings.TrimSpace(candidate.VersionID) == "" ||
			strings.TrimSpace(candidate.Filename) == "" || strings.TrimSpace(candidate.ContentType) == "" ||
			candidate.SizeBytes < 0 || strings.TrimSpace(candidate.Checksum) == "" {
			return nil, ErrEventConflict
		}
		refs = append(refs, UserArtifactReferenceInput{ArtifactID: candidate.ArtifactID, VersionID: candidate.VersionID})
	}
	return refs, nil
}

func stringValueFromAny(value any) string {
	text, _ := value.(string)
	return text
}
