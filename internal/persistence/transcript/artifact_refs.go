package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxArtifactReferencesPerAttempt = 256

type runnerToolArtifactSourcePayload struct {
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	ToolPhase  string `json:"toolPhase"`
}

type runnerToolArtifactModelCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type runnerToolArtifactModelCallsPayload struct {
	ModelToolCalls []runnerToolArtifactModelCall `json:"modelToolCalls"`
}

func runnerToolArtifactSourcePhase(phase string) bool {
	return phase == "start" || phase == "verification_tool"
}

// ValidateRunnerToolArtifactSource authorizes one exact immutable runner
// checkpoint as the source of an artifact write. The live claim and the
// checkpoint's tool-call identity are checked in the same immediate read
// transaction; the workspace write rechecks the claim in its commit.
func (r *Repository) ValidateRunnerToolArtifactSource(
	ctx context.Context,
	claim RunnerClaim,
	sourceEventID int64,
	toolCallID, toolName string,
) (Stream, error) {
	toolCallID = strings.TrimSpace(toolCallID)
	toolName = strings.TrimSpace(toolName)
	if sourceEventID <= 0 || toolCallID == "" || toolName == "" || len(toolCallID) > 512 || len(toolName) > 512 {
		return Stream{}, ErrEventConflict
	}
	var stream Stream
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		var err error
		stream, err = validateClaimConn(ctx, conn, claim, r.now().UTC(), false)
		if err != nil {
			return err
		}
		var eventType string
		var runnerAttempt sql.NullInt64
		var payloadJSON []byte
		if err := conn.QueryRowContext(ctx, `
			SELECT event_type,runner_attempt,payload_json
			FROM transcript_events WHERE stream_uid=? AND event_id=?`,
			stream.UID, sourceEventID,
		).Scan(&eventType, &runnerAttempt, &payloadJSON); err != nil {
			return ErrClaimStale
		}
		var payload runnerToolArtifactSourcePayload
		if eventType != "runner_checkpoint" || !runnerAttempt.Valid || runnerAttempt.Int64 != claim.Attempt ||
			json.Unmarshal(payloadJSON, &payload) != nil || payload.ToolCallID != toolCallID ||
			payload.ToolName != toolName || !runnerToolArtifactSourcePhase(payload.ToolPhase) {
			return ErrClaimStale
		}
		return nil
	})
	if err != nil {
		return Stream{}, schemaError(err)
	}
	return stream, nil
}

// ValidateDurableRunnerToolArtifactSource authorizes the immutable checkpoint
// bound to a durable kernel operation after the original runner lease has
// expired. It deliberately does not recreate or accept a live claim token;
// the operation's persisted source event and runner attempt are the recovery
// authority, while the workspace operation layer performs its own frame and
// branch authority checks before terminal settlement.
func (r *Repository) ValidateDurableRunnerToolArtifactSource(
	ctx context.Context,
	streamUID, ownerID string,
	sourceEventID, runnerAttempt int64,
	toolCallID, toolName string,
) (Stream, error) {
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	toolCallID = strings.TrimSpace(toolCallID)
	toolName = strings.TrimSpace(toolName)
	if r == nil || r.db == nil || sourceEventID <= 0 || runnerAttempt <= 0 ||
		streamUID == "" || ownerID == "" || toolCallID == "" || toolName == "" ||
		len(toolCallID) > 512 || len(toolName) > 512 {
		return Stream{}, ErrEventConflict
	}
	stream, err := r.GetStream(ctx, streamUID, ownerID)
	if err != nil {
		return Stream{}, err
	}
	var eventType string
	var storedAttempt sql.NullInt64
	var payloadJSON []byte
	err = r.db.QueryRowContext(ctx, `
		SELECT event_type,runner_attempt,payload_json
		FROM transcript_events WHERE stream_uid=? AND event_id=?`,
		stream.UID, sourceEventID,
	).Scan(&eventType, &storedAttempt, &payloadJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Stream{}, ErrEventConflict
	}
	if err != nil {
		return Stream{}, schemaError(err)
	}
	if eventType != "runner_checkpoint" || !storedAttempt.Valid || storedAttempt.Int64 != runnerAttempt ||
		!durableRunnerToolArtifactSourceMatches(payloadJSON, toolCallID, toolName) {
		return Stream{}, ErrEventConflict
	}
	return stream, nil
}

func durableRunnerToolArtifactSourceMatches(payloadJSON []byte, toolCallID, toolName string) bool {
	var direct runnerToolArtifactSourcePayload
	if json.Unmarshal(payloadJSON, &direct) == nil && direct.ToolCallID == toolCallID &&
		direct.ToolName == toolName && runnerToolArtifactSourcePhase(direct.ToolPhase) {
		return true
	}
	var batch runnerToolArtifactModelCallsPayload
	if json.Unmarshal(payloadJSON, &batch) != nil {
		return false
	}
	for _, call := range batch.ModelToolCalls {
		if call.ID == toolCallID && call.Name == toolName {
			return true
		}
	}
	return false
}

// FindArtifactCommitForToolCall returns the single durable idempotency receipt
// for a deterministic artifact/tool identity, including commits made before a
// process restart. Any changed source identity or version is a conflict.
func (r *Repository) FindArtifactCommitForToolCall(
	ctx context.Context,
	streamUID, ownerID, artifactID, toolCallID, toolName string,
) (ArtifactReference, bool, error) {
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	artifactID = strings.TrimSpace(artifactID)
	toolCallID = strings.TrimSpace(toolCallID)
	toolName = strings.TrimSpace(toolName)
	if streamUID == "" || ownerID == "" || artifactID == "" || toolCallID == "" || toolName == "" {
		return ArtifactReference{}, false, ErrEventConflict
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return ArtifactReference{}, false, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT commit_row.stream_uid,commit_row.runner_attempt,commit_row.source_event_id,
			commit_row.ordinal,commit_row.artifact_id,commit_row.version_id,
			commit_row.relation,commit_row.created_at,event.payload_json
		FROM transcript_artifact_commits commit_row
		JOIN transcript_events event
			ON event.stream_uid=commit_row.stream_uid
			AND event.event_id=commit_row.source_event_id
			AND event.runner_attempt=commit_row.runner_attempt
		WHERE commit_row.stream_uid=? AND commit_row.artifact_id=?
		ORDER BY commit_row.runner_attempt,commit_row.source_event_id,commit_row.ordinal`,
		streamUID, artifactID,
	)
	if err != nil {
		return ArtifactReference{}, false, schemaError(err)
	}
	defer rows.Close()
	var result ArtifactReference
	found := false
	for rows.Next() {
		var candidate ArtifactReference
		var relation string
		var payloadJSON []byte
		if err := rows.Scan(
			&candidate.StreamUID, &candidate.RunnerAttempt, &candidate.SourceEventID,
			&candidate.Ordinal, &candidate.ArtifactID, &candidate.VersionID,
			&relation, &candidate.CreatedAt, &payloadJSON,
		); err != nil {
			return ArtifactReference{}, false, schemaError(err)
		}
		candidate.Relation = ArtifactRelation(relation)
		candidate.Availability = ArtifactAvailable
		var payload runnerToolArtifactSourcePayload
		if json.Unmarshal(payloadJSON, &payload) != nil || payload.ToolCallID != toolCallID ||
			payload.ToolName != toolName || !runnerToolArtifactSourcePhase(payload.ToolPhase) ||
			candidate.Relation != ArtifactRelationProduced || found {
			return ArtifactReference{}, false, ErrEventConflict
		}
		result, found = candidate, true
	}
	if err := rows.Err(); err != nil {
		return ArtifactReference{}, false, schemaError(err)
	}
	return result, found, nil
}

// AppendAssistantEventWithCommittedArtifacts binds only artifact versions that
// were committed under this exact runner claim and a durable source event.
func (r *Repository) AppendAssistantEventWithCommittedArtifacts(
	ctx context.Context,
	input AppendEventInput,
) (Event, []ArtifactReference, bool, error) {
	if input.Type != "assistant_message" {
		return Event{}, nil, false, errors.New("assistant_message event is required")
	}
	if err := validateRunnerEventInput(input); err != nil {
		return Event{}, nil, false, err
	}
	var event Event
	var refs []ArtifactReference
	var created bool
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		now := r.now().UTC()
		stream, err := getStreamConn(ctx, conn, input.Claim.StreamUID, input.Claim.OwnerID)
		if err != nil {
			return err
		}
		record := eventRecord{
			clientMessageID: input.ClientMessageID, eventType: input.Type, source: input.Source,
			runnerAttempt: &input.Claim.Attempt, payloadJSON: input.PayloadJSON,
			frameEventID: input.FrameEventID, destinations: input.Destinations, createdAt: now,
		}
		if existing, found, err := findEventByClientID(ctx, conn, stream.UID, input.ClientMessageID); err != nil {
			return err
		} else if found {
			if _, err := validateClaimConn(ctx, conn, input.Claim, now, false); err != nil {
				return err
			}
			if !eventMatches(existing, record) {
				return ErrEventConflict
			}
			destinations, err := deliveryDestinations(ctx, conn, existing.StreamUID, existing.PublicationSeq)
			if err != nil {
				return err
			}
			if !stringSlicesEqual(destinations, normalizedDestinations(input.Destinations)) {
				return ErrEventConflict
			}
			event = existing
		} else {
			stream, err = validateClaimConn(ctx, conn, input.Claim, now, true)
			if err != nil {
				return err
			}
			if err := validateFrameEventReferenceConn(ctx, conn, stream, input.Type, input.Source, input.FrameEventID); err != nil {
				return err
			}
			event, created, err = appendEventConn(ctx, conn, stream, record)
			if err != nil {
				return err
			}
		}
		refs, err = bindCommittedArtifactReferencesConn(ctx, conn, stream, input.Claim.Attempt, event.EventID, now)
		return err
	})
	if err != nil {
		return Event{}, nil, false, schemaError(err)
	}
	return event, refs, created, nil
}

func (r *Repository) AppendAssistantEventWithArtifacts(
	ctx context.Context,
	input AppendAssistantEventWithArtifactsInput,
) (Event, []ArtifactReference, bool, error) {
	eventInput := AppendEventInput{
		Claim: input.Claim, ClientMessageID: input.ClientMessageID, Type: "assistant_message",
		Source: input.Source, PayloadJSON: input.PayloadJSON, FrameEventID: input.FrameEventID,
		Destinations: input.Destinations,
	}
	if err := validateRunnerEventInput(eventInput); err != nil {
		return Event{}, nil, false, err
	}
	if len(input.References) == 0 || len(input.References) > maxArtifactReferencesPerAttempt {
		return Event{}, nil, false, fmt.Errorf("1-%d artifact references are required", maxArtifactReferencesPerAttempt)
	}
	normalized, err := normalizeArtifactReferenceInputs(input.References)
	if err != nil {
		return Event{}, nil, false, err
	}
	var event Event
	var refs []ArtifactReference
	var created bool
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		now := r.now().UTC()
		stream, err := getStreamConn(ctx, conn, input.Claim.StreamUID, input.Claim.OwnerID)
		if err != nil {
			return err
		}
		if existingEvent, found, err := findEventByClientID(ctx, conn, stream.UID, input.ClientMessageID); err != nil {
			return err
		} else if found {
			if _, err := validateClaimConn(ctx, conn, input.Claim, now, false); err != nil {
				return err
			}
			record := eventRecord{
				clientMessageID: input.ClientMessageID, eventType: "assistant_message", source: input.Source,
				runnerAttempt: &input.Claim.Attempt, payloadJSON: input.PayloadJSON,
				frameEventID: input.FrameEventID, destinations: input.Destinations, createdAt: existingEvent.CreatedAt,
			}
			if !eventMatches(existingEvent, record) {
				return ErrEventConflict
			}
			destinations, err := deliveryDestinations(ctx, conn, stream.UID, existingEvent.PublicationSeq)
			if err != nil {
				return err
			}
			if !stringSlicesEqual(destinations, normalizedDestinations(input.Destinations)) {
				return ErrEventConflict
			}
			existingRefs, err := listArtifactReferencesForEventConn(ctx, conn, stream.UID, input.Claim.Attempt, existingEvent.EventID)
			if err != nil {
				return err
			}
			if !artifactReferencesMatch(existingRefs, existingEvent.EventID, normalized) {
				return ErrEventConflict
			}
			event = existingEvent
			refs = existingRefs
			return nil
		}
		stream, err = validateClaimConn(ctx, conn, input.Claim, now, true)
		if err != nil {
			return err
		}
		if err := validateFrameEventReferenceConn(ctx, conn, stream, "assistant_message", input.Source, input.FrameEventID); err != nil {
			return err
		}
		event, created, err = appendEventConn(ctx, conn, stream, eventRecord{
			clientMessageID: input.ClientMessageID, eventType: "assistant_message", source: input.Source,
			runnerAttempt: &input.Claim.Attempt, payloadJSON: input.PayloadJSON,
			frameEventID: input.FrameEventID, destinations: input.Destinations, createdAt: now,
		})
		if err != nil {
			return err
		}
		existing, err := listArtifactReferencesForEventConn(ctx, conn, stream.UID, input.Claim.Attempt, event.EventID)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			if artifactReferencesMatch(existing, event.EventID, normalized) {
				refs = existing
				return nil
			}
			return ErrEventConflict
		}
		if !created {
			return ErrEventConflict
		}
		refs, err = insertArtifactReferencesConn(ctx, conn, stream, input.Claim.Attempt, event.EventID, normalized, now)
		return err
	})
	if err != nil {
		return Event{}, nil, false, schemaError(err)
	}
	return event, refs, created, nil
}

const artifactReferenceSelectSQL = `
	SELECT ref.stream_uid,ref.runner_attempt,ref.source_event_id,ref.ordinal,
		ref.artifact_id,ref.version_id,ref.relation,
		CASE
			WHEN tombstone.version_id IS NOT NULL OR ref.availability='deleted' THEN 'deleted'
			WHEN project.id IS NULL OR version.id IS NULL OR artifact.id IS NULL THEN 'missing'
			ELSE ref.availability
		END,
		ref.created_at
	FROM transcript_artifact_refs ref
	JOIN transcript_streams stream ON stream.stream_uid=ref.stream_uid
	LEFT JOIN artifact_version_tombstones tombstone
		ON tombstone.artifact_id=ref.artifact_id AND tombstone.version_id=ref.version_id
		AND tombstone.owner_id=stream.owner_id AND tombstone.project_id=stream.project_id
	LEFT JOIN projects project ON project.id=stream.project_id AND project.user_id=stream.owner_id
	LEFT JOIN artifact_versions version ON version.id=ref.version_id AND version.artifact_id=ref.artifact_id
	LEFT JOIN artifacts artifact ON artifact.id=ref.artifact_id AND artifact.project_id=project.id`

const listArtifactReferencesSQL = artifactReferenceSelectSQL + `
	WHERE ref.stream_uid=? AND ref.runner_attempt=? ORDER BY ref.source_event_id,ref.ordinal`

const listArtifactReferencesForEventSQL = artifactReferenceSelectSQL + `
	WHERE ref.stream_uid=? AND ref.runner_attempt=? AND ref.source_event_id=? ORDER BY ref.ordinal`

func insertArtifactReferencesConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	attempt, sourceEventID int64,
	values []ArtifactReferenceInput,
	createdAt time.Time,
) ([]ArtifactReference, error) {
	refs := make([]ArtifactReference, len(values))
	for index, ref := range values {
		if err := validateArtifactVersionAuthority(ctx, conn, stream, ref); err != nil {
			return nil, err
		}
		refs[index] = ArtifactReference{
			StreamUID: stream.UID, RunnerAttempt: attempt, SourceEventID: sourceEventID,
			Ordinal: index, ArtifactID: ref.ArtifactID, VersionID: ref.VersionID,
			Relation: ref.Relation, Availability: ArtifactAvailable, CreatedAt: createdAt,
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO transcript_artifact_refs(
				stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,availability,created_at
			) VALUES(?,?,?,?,?,?,?,?,?)`,
			refs[index].StreamUID, refs[index].RunnerAttempt, refs[index].SourceEventID, refs[index].Ordinal,
			refs[index].ArtifactID, refs[index].VersionID, string(refs[index].Relation),
			string(refs[index].Availability), refs[index].CreatedAt,
		); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

type committedArtifactReference struct {
	sourceEventID int64
	ordinal       int
	artifactID    string
	versionID     string
	relation      ArtifactRelation
}

func bindCommittedArtifactReferencesConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	attempt, targetEventID int64,
	createdAt time.Time,
) ([]ArtifactReference, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT source_event_id,ordinal,artifact_id,version_id,relation
		FROM transcript_artifact_commits
		WHERE stream_uid=? AND runner_attempt=? AND bound_event_id IS NULL
		ORDER BY source_event_id,ordinal,artifact_id,version_id`, stream.UID, attempt)
	if err != nil {
		return nil, err
	}
	commits := []committedArtifactReference{}
	for rows.Next() {
		var value committedArtifactReference
		if err := rows.Scan(&value.sourceEventID, &value.ordinal, &value.artifactID, &value.versionID, &value.relation); err != nil {
			_ = rows.Close()
			return nil, err
		}
		commits = append(commits, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	allCommitCount := len(commits)
	// One assistant answer presents the current head of each artifact relation,
	// not every immutable version written while the task was refining it. Keep
	// all commits for the durable bound_event_id receipt below, but bind only the
	// last source-ordered version for each artifact/relation pair to the public
	// message. This is the same head rule used by CurrentArtifactCommitSnapshot.
	latestCommits := make([]committedArtifactReference, 0, len(commits))
	latestPositions := make(map[string]int, len(commits))
	for _, commit := range commits {
		key := commit.artifactID + "\x00" + string(commit.relation)
		if index, found := latestPositions[key]; found {
			latestCommits[index] = commit
			continue
		}
		latestPositions[key] = len(latestCommits)
		latestCommits = append(latestCommits, commit)
	}
	commits = latestCommits
	existing, err := listArtifactReferencesForEventConn(ctx, conn, stream.UID, attempt, targetEventID)
	if err != nil {
		return nil, err
	}
	type boundArtifactHead struct {
		versionID string
		relation  ArtifactRelation
	}
	heads := make(map[string]boundArtifactHead, len(existing)+len(commits))
	for _, ref := range existing {
		key := ref.ArtifactID + "\x00" + string(ref.Relation)
		if prior, found := heads[key]; found && prior.versionID != ref.VersionID {
			return nil, ErrEventConflict
		}
		heads[key] = boundArtifactHead{versionID: ref.VersionID, relation: ref.Relation}
	}
	refs := append([]ArtifactReference(nil), existing...)
	for _, commit := range commits {
		key := commit.artifactID + "\x00" + string(commit.relation)
		if head, found := heads[key]; found {
			if head.relation != commit.relation || head.versionID != commit.versionID {
				return nil, ErrEventConflict
			}
			continue
		}
		if len(refs) >= maxArtifactReferencesPerAttempt {
			return nil, ErrEventConflict
		}
		input := ArtifactReferenceInput{ArtifactID: commit.artifactID, VersionID: commit.versionID, Relation: commit.relation}
		if err := validateArtifactVersionAuthority(ctx, conn, stream, input); err != nil {
			return nil, err
		}
		ref := ArtifactReference{
			StreamUID: stream.UID, RunnerAttempt: attempt, SourceEventID: targetEventID,
			Ordinal: len(refs), ArtifactID: commit.artifactID, VersionID: commit.versionID,
			Relation: commit.relation, Availability: ArtifactAvailable, CreatedAt: createdAt,
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO transcript_artifact_refs(
				stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,availability,created_at
			) VALUES(?,?,?,?,?,?,?,?,?)`,
			ref.StreamUID, ref.RunnerAttempt, ref.SourceEventID, ref.Ordinal, ref.ArtifactID, ref.VersionID,
			string(ref.Relation), string(ref.Availability), ref.CreatedAt,
		); err != nil {
			return nil, err
		}
		heads[key] = boundArtifactHead{versionID: commit.versionID, relation: commit.relation}
		refs = append(refs, ref)
	}
	if allCommitCount > 0 {
		result, err := conn.ExecContext(ctx, `
			UPDATE transcript_artifact_commits SET bound_event_id=?
			WHERE stream_uid=? AND runner_attempt=? AND bound_event_id IS NULL`,
			targetEventID, stream.UID, attempt)
		if err != nil {
			return nil, err
		}
		if affected, err := result.RowsAffected(); err != nil || affected != int64(allCommitCount) {
			if err != nil {
				return nil, err
			}
			return nil, ErrEventConflict
		}
	}
	return refs, nil
}

func (r *Repository) ListArtifactReferences(
	ctx context.Context,
	streamUID, ownerID string,
	attempt int64,
) ([]ArtifactReference, error) {
	if attempt <= 0 {
		return nil, errors.New("positive runner attempt is required")
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, listArtifactReferencesSQL, streamUID, attempt)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()
	refs := []ArtifactReference{}
	for rows.Next() {
		var ref ArtifactReference
		var relation, availability string
		if err := rows.Scan(
			&ref.StreamUID, &ref.RunnerAttempt, &ref.SourceEventID, &ref.Ordinal,
			&ref.ArtifactID, &ref.VersionID, &relation, &availability, &ref.CreatedAt,
		); err != nil {
			return nil, schemaError(err)
		}
		ref.Relation = ArtifactRelation(relation)
		ref.Availability = ArtifactAvailability(availability)
		refs = append(refs, ref)
	}
	return refs, schemaError(rows.Err())
}

// ArtifactReferenceRevision returns the monotonic structural revision of a
// stream's artifact-reference ledger in O(1). Availability remains a live
// projection and therefore does not participate in this revision.
func (r *Repository) ArtifactReferenceRevision(ctx context.Context, streamUID, ownerID string) (int64, error) {
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return 0, err
	}
	var revision int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT revision FROM transcript_artifact_reference_heads WHERE stream_uid=?`, streamUID,
	).Scan(&revision); err != nil {
		return 0, schemaError(err)
	}
	return revision, nil
}

// ListArtifactCommitReferences returns the immutable artifact versions
// committed by one exact runner attempt, before or after they are bound to an
// assistant/terminal event. Completion gates use this ledger rather than tool
// text or project-wide artifact existence.
func (r *Repository) ListArtifactCommitReferences(
	ctx context.Context,
	streamUID, ownerID string,
	attempt int64,
) ([]ArtifactReferenceInput, error) {
	if attempt <= 0 {
		return nil, errors.New("positive runner attempt is required")
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT artifact_id,version_id,relation
		FROM transcript_artifact_commits
		WHERE stream_uid=? AND runner_attempt=?
		ORDER BY source_event_id,ordinal,artifact_id,version_id`, streamUID, attempt)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()
	result := []ArtifactReferenceInput{}
	for rows.Next() {
		var value ArtifactReferenceInput
		var relation string
		if err := rows.Scan(&value.ArtifactID, &value.VersionID, &relation); err != nil {
			return nil, schemaError(err)
		}
		value.Relation = ArtifactRelation(relation)
		result = append(result, value)
	}
	return result, schemaError(rows.Err())
}

// CurrentArtifactCommitSnapshot returns the latest immutable commit for each
// artifact identity and relation visible on the active branch through one
// runner attempt. Stream ownership, branch state, membership and heads are read
// in one transaction so abandoned-branch or future-attempt artifacts cannot
// leak into a resumed completion gate.
func (r *Repository) CurrentArtifactCommitSnapshot(
	ctx context.Context,
	streamUID, ownerID string,
	throughAttempt int64,
) (ArtifactCommitSnapshot, error) {
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if throughAttempt <= 0 {
		return ArtifactCommitSnapshot{}, errors.New("positive through runner attempt is required")
	}
	if r == nil || r.db == nil {
		return ArtifactCommitSnapshot{}, ErrSchemaUnavailable
	}
	if streamUID == "" || ownerID == "" {
		return ArtifactCommitSnapshot{}, errors.New("stream and owner are required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ArtifactCommitSnapshot{}, schemaError(err)
	}
	defer func() { _ = tx.Rollback() }()
	result := ArtifactCommitSnapshot{StreamUID: streamUID, ThroughAttempt: throughAttempt}
	var storedOwner string
	if err := tx.QueryRowContext(ctx, `
		SELECT stream.owner_id,state.active_branch_id,state.generation
		FROM transcript_streams stream
		JOIN transcript_branch_state state ON state.stream_uid=stream.stream_uid
		WHERE stream.stream_uid=?`, streamUID,
	).Scan(&storedOwner, &result.BranchID, &result.BranchGeneration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ArtifactCommitSnapshot{}, ErrEventConflict
		}
		return ArtifactCommitSnapshot{}, schemaError(err)
	}
	if storedOwner != ownerID {
		return ArtifactCommitSnapshot{}, ErrOwnerMismatch
	}
	rows, err := tx.QueryContext(ctx, `
		WITH ranked AS (
			SELECT commit_row.runner_attempt,commit_row.source_event_id,commit_row.ordinal,
				commit_row.artifact_id,commit_row.version_id,commit_row.relation,
				commit_row.created_at,membership.ordinal AS branch_ordinal,
				ROW_NUMBER() OVER (
					PARTITION BY commit_row.artifact_id,commit_row.relation
					ORDER BY membership.ordinal DESC,commit_row.ordinal DESC,commit_row.version_id DESC
				) AS current_rank
			FROM transcript_artifact_commits commit_row
			JOIN transcript_branch_events membership
				ON membership.stream_uid=commit_row.stream_uid
				AND membership.event_id=commit_row.source_event_id
			WHERE commit_row.stream_uid=? AND membership.branch_id=?
				AND commit_row.runner_attempt<=?
		)
		SELECT current_row.runner_attempt,current_row.source_event_id,current_row.ordinal,
			current_row.artifact_id,current_row.version_id,current_row.relation,
			CASE
				WHEN tombstone.version_id IS NOT NULL THEN 'deleted'
				WHEN project.id IS NULL OR version.id IS NULL OR artifact.id IS NULL THEN 'missing'
				ELSE 'available'
			END,current_row.created_at
		FROM ranked current_row
		JOIN transcript_streams stream ON stream.stream_uid=?
		LEFT JOIN artifact_version_tombstones tombstone
			ON tombstone.artifact_id=current_row.artifact_id AND tombstone.version_id=current_row.version_id
			AND tombstone.owner_id=stream.owner_id AND tombstone.project_id=stream.project_id
		LEFT JOIN projects project ON project.id=stream.project_id AND project.user_id=stream.owner_id
		LEFT JOIN artifact_versions version
			ON version.id=current_row.version_id AND version.artifact_id=current_row.artifact_id
		LEFT JOIN artifacts artifact
			ON artifact.id=current_row.artifact_id AND artifact.project_id=project.id
		WHERE current_row.current_rank=1
		ORDER BY current_row.branch_ordinal,current_row.ordinal,current_row.artifact_id,current_row.version_id`,
		streamUID, result.BranchID, throughAttempt, streamUID,
	)
	if err != nil {
		return ArtifactCommitSnapshot{}, schemaError(err)
	}
	refs := []ArtifactReference{}
	for rows.Next() {
		var value ArtifactReference
		var relation, availability string
		if err := rows.Scan(
			&value.RunnerAttempt, &value.SourceEventID, &value.Ordinal,
			&value.ArtifactID, &value.VersionID, &relation, &availability, &value.CreatedAt,
		); err != nil {
			_ = rows.Close()
			return ArtifactCommitSnapshot{}, schemaError(err)
		}
		value.StreamUID = streamUID
		value.Relation = ArtifactRelation(relation)
		value.Availability = ArtifactAvailability(availability)
		refs = append(refs, value)
	}
	if err := rows.Close(); err != nil {
		return ArtifactCommitSnapshot{}, schemaError(err)
	}
	if err := rows.Err(); err != nil {
		return ArtifactCommitSnapshot{}, schemaError(err)
	}
	result.References = refs
	if err := tx.Commit(); err != nil {
		return ArtifactCommitSnapshot{}, schemaError(err)
	}
	return result, nil
}

// ListArtifactCommitReferencesForEvent returns the canonical artifact commits
// made for one exact runner tool-source event across runner reclaim attempts.
// It is the durable idempotency receipt for host tools that must replay without
// repeating external side effects.
func (r *Repository) ListArtifactCommitReferencesForEvent(
	ctx context.Context,
	streamUID, ownerID string,
	sourceEventID int64,
) ([]ArtifactReference, error) {
	if sourceEventID <= 0 {
		return nil, errors.New("positive source event id is required")
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at
		FROM transcript_artifact_commits
		WHERE stream_uid=? AND source_event_id=?
		ORDER BY runner_attempt,ordinal,artifact_id,version_id`, streamUID, sourceEventID)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()
	result := []ArtifactReference{}
	for rows.Next() {
		var value ArtifactReference
		var relation string
		if err := rows.Scan(
			&value.StreamUID, &value.RunnerAttempt, &value.SourceEventID, &value.Ordinal,
			&value.ArtifactID, &value.VersionID, &relation, &value.CreatedAt,
		); err != nil {
			return nil, schemaError(err)
		}
		value.Relation = ArtifactRelation(relation)
		result = append(result, value)
	}
	return result, schemaError(rows.Err())
}

func (r *Repository) ListArtifactReferenceBacklinks(
	ctx context.Context,
	ownerID, artifactID, versionID string,
	limit int,
) ([]ArtifactReference, error) {
	if r == nil || r.db == nil {
		return nil, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	artifactID = strings.TrimSpace(artifactID)
	versionID = strings.TrimSpace(versionID)
	if ownerID == "" || artifactID == "" || versionID == "" || limit <= 0 || limit > 1000 {
		return nil, errors.New("owner, artifact, version, and bounded limit are required")
	}
	rows, err := r.db.QueryContext(ctx, artifactReferenceSelectSQL+`
		WHERE stream.owner_id=? AND ref.artifact_id=? AND ref.version_id=?
		ORDER BY ref.stream_uid,ref.runner_attempt,ref.source_event_id,ref.ordinal LIMIT ?`,
		ownerID, artifactID, versionID, limit)
	refs, err := scanArtifactReferences(rows, err)
	return refs, schemaError(err)
}

func (r *Repository) MarkArtifactVersionUnavailable(
	ctx context.Context,
	ownerID, artifactID, versionID string,
	availability ArtifactAvailability,
) (int64, error) {
	ownerID = strings.TrimSpace(ownerID)
	artifactID = strings.TrimSpace(artifactID)
	versionID = strings.TrimSpace(versionID)
	if ownerID == "" || artifactID == "" || versionID == "" ||
		(availability != ArtifactDeleted && availability != ArtifactMissing) {
		return 0, errors.New("owner, artifact, version, and unavailable status are required")
	}
	var affected int64
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `
			UPDATE transcript_artifact_refs SET availability=?
			WHERE artifact_id=? AND version_id=? AND stream_uid IN (
				SELECT stream_uid FROM transcript_streams WHERE owner_id=?
			)`, string(availability), artifactID, versionID, ownerID)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, schemaError(err)
}

func validateArtifactVersionAuthority(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	ref ArtifactReferenceInput,
) error {
	var projectID, ownerID, metadataRootFrameID, producingRootFrameID, frameID string
	err := conn.QueryRowContext(ctx, `
		SELECT artifact.project_id,project.user_id,
			COALESCE(runtime.root_frame_id,''),COALESCE(frame.root_frame_id,''),
			COALESCE(provenance.frame_id,runtime.frame_id,'')
		FROM artifacts artifact
		JOIN artifact_versions version ON version.artifact_id=artifact.id
		JOIN projects project ON project.id=artifact.project_id
		LEFT JOIN artifact_runtime_metadata runtime ON runtime.artifact_id=artifact.id
		LEFT JOIN artifact_version_provenance provenance ON provenance.version_id=version.id
		LEFT JOIN frames frame ON frame.id=COALESCE(provenance.frame_id,runtime.frame_id)
		WHERE artifact.id=? AND version.id=?`, ref.ArtifactID, ref.VersionID,
	).Scan(&projectID, &ownerID, &metadataRootFrameID, &producingRootFrameID, &frameID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrArtifactMissing
	}
	if err != nil {
		return err
	}
	if ownerID != stream.OwnerID || projectID != stream.ProjectID || frameID == "" ||
		metadataRootFrameID != stream.RootFrameID || producingRootFrameID != stream.RootFrameID {
		return ErrArtifactMismatch
	}
	if (ref.Relation == ArtifactRelationProduced || ref.Relation == ArtifactRelationAttached) && frameID != stream.FrameID {
		return ErrArtifactMismatch
	}
	return nil
}

func normalizeArtifactReferenceInputs(values []ArtifactReferenceInput) ([]ArtifactReferenceInput, error) {
	refs := make([]ArtifactReferenceInput, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		value.ArtifactID = strings.TrimSpace(value.ArtifactID)
		value.VersionID = strings.TrimSpace(value.VersionID)
		if value.ArtifactID == "" || value.VersionID == "" || len(value.ArtifactID) > 512 || len(value.VersionID) > 512 ||
			!validArtifactRelation(value.Relation) {
			return nil, errors.New("artifact id, version id, and relation are required")
		}
		key := value.ArtifactID + "\x00" + value.VersionID
		if _, duplicate := seen[key]; duplicate {
			return nil, ErrEventConflict
		}
		seen[key] = struct{}{}
		refs[index] = value
	}
	return refs, nil
}

func artifactReferencesMatch(existing []ArtifactReference, sourceEventID int64, expected []ArtifactReferenceInput) bool {
	if len(existing) != len(expected) {
		return false
	}
	for index := range existing {
		if existing[index].SourceEventID != sourceEventID || existing[index].Ordinal != index ||
			existing[index].ArtifactID != expected[index].ArtifactID || existing[index].VersionID != expected[index].VersionID ||
			existing[index].Relation != expected[index].Relation {
			return false
		}
	}
	return true
}

type transcriptQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func listArtifactReferencesConn(ctx context.Context, queryer transcriptQueryer, streamUID string, attempt int64) ([]ArtifactReference, error) {
	rows, err := queryer.QueryContext(ctx, listArtifactReferencesSQL, streamUID, attempt)
	return scanArtifactReferences(rows, err)
}

func listArtifactReferencesForEventConn(
	ctx context.Context,
	queryer transcriptQueryer,
	streamUID string,
	attempt, sourceEventID int64,
) ([]ArtifactReference, error) {
	rows, err := queryer.QueryContext(ctx, listArtifactReferencesForEventSQL, streamUID, attempt, sourceEventID)
	return scanArtifactReferences(rows, err)
}

func scanArtifactReferences(rows *sql.Rows, err error) ([]ArtifactReference, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []ArtifactReference{}
	for rows.Next() {
		var ref ArtifactReference
		var relation, availability string
		if err := rows.Scan(
			&ref.StreamUID, &ref.RunnerAttempt, &ref.SourceEventID, &ref.Ordinal,
			&ref.ArtifactID, &ref.VersionID, &relation, &availability, &ref.CreatedAt,
		); err != nil {
			return nil, err
		}
		ref.Relation = ArtifactRelation(relation)
		ref.Availability = ArtifactAvailability(availability)
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func validArtifactRelation(relation ArtifactRelation) bool {
	return relation == ArtifactRelationProduced || relation == ArtifactRelationConsumed ||
		relation == ArtifactRelationCited || relation == ArtifactRelationAttached
}
