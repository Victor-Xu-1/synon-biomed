package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strings"
	"time"
)

func writeHistoryDigestBufferField(buffer *bytes.Buffer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = buffer.Write(length[:])
	_, _ = buffer.Write(value)
}

type PrepareAskUserHistoryCutoverInput struct {
	BackfillID    string
	StreamUID     string
	OwnerID       string
	MaxBranches   int
	MaxEvents     int
	MaxCursorRows int
	MaxShadowRows int
}

type AskUserHistoryCutover struct {
	CutoverID                        string
	BackfillID                       string
	SupersedesCutoverID              string
	StreamUID                        string
	OwnerID                          string
	SourceBranchID                   string
	SourceGeneration                 int64
	SourceThroughPublicationSequence int64
	VerificationSHA256               string
	LineageSHA256                    string
	CursorSHA256                     string
	ShadowSHA256                     string
	BranchCount                      int
	EventCount                       int
	CursorCount                      int
	Ready                            bool
	CreatedAt                        time.Time
}

type ResolveHistoryCutoverCursorInput struct {
	CutoverID                        string
	OwnerID                          string
	SourceBranchID                   string
	SourceGeneration                 int64
	SourceThroughPublicationSequence int64
	SourceMessageIndex               int64
}

type HistoryCutoverCursorResolution struct {
	TargetBranchID            string
	StableMessageID           string
	TargetPublicationSequence int64
	TargetMessageIndex        int64
}

type historyCutoverEvent struct {
	Key, FactKind, ClientMessageID, EventType string
	SourceEventID                             *int64
	CandidateID                               string
	RunnerAttempt                             *int64
	PayloadJSON                               []byte
	PayloadSHA256                             string
	CreatedAt                                 time.Time
	TargetPublicationSequence                 int64
	rank                                      int64
}

type historyCutoverMembership struct {
	TargetOrdinal int64
	EventKey      string
	SourceOrdinal *int64
}

type historyCutoverBranch struct {
	SourceID, TargetID, ParentSourceID, ParentTargetID, Kind string
	SourceForkEventID                                        *int64
	TargetForkEventKey                                       string
	ForkPoint                                                int64
	ClientMutationID, RequestSHA256, SourceMessageID         string
	CreatedAt, UpdatedAt                                     time.Time
	SourceThroughOrdinal, SourceThroughPublicationSequence   int64
	SourceMembershipSHA256, TargetMembershipSHA256           string
	Memberships                                              []historyCutoverMembership
	SourceEventTargetKeys                                    map[int64]string
}

type historyCutoverCursor struct {
	SourceBranchID, TargetBranchID, StableMessageID    string
	SourceGeneration, SourceThroughPublicationSequence int64
	SourceMessageIndex, SourceOrdinal                  int64
	TargetPublicationSequence, TargetMessageIndex      int64
}

type historyCutoverShadow struct {
	SourceBranchID, Dimension, Digest string
}

type historyCutoverPlan struct {
	Cutover   AskUserHistoryCutover
	Events    []historyCutoverEvent
	Branches  []historyCutoverBranch
	Cursors   []historyCutoverCursor
	Shadows   []historyCutoverShadow
	Receipts  []historyCutoverReceipt
	Artifacts []historyCutoverArtifactReference
}

type historyCutoverReceipt struct {
	SourceAttempt          int64
	TargetEventKey, Status string
	FinishedAt             time.Time
}

type historyCutoverArtifactReference struct {
	TargetEventKey                                string
	Ordinal                                       int
	ArtifactID, VersionID, Relation, Availability string
	CreatedAt                                     time.Time
}

type historySourceEvent struct {
	Event
	Ordinal int64
}

func (r *Repository) PrepareAskUserHistoryCutover(
	ctx context.Context,
	input PrepareAskUserHistoryCutoverInput,
) (AskUserHistoryCutover, bool, error) {
	if r == nil || r.db == nil {
		return AskUserHistoryCutover{}, false, ErrSchemaUnavailable
	}
	input.BackfillID = strings.TrimSpace(input.BackfillID)
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	if len(input.BackfillID) != sha256.Size*2 || input.StreamUID == "" || input.OwnerID == "" ||
		input.MaxBranches <= 0 || input.MaxBranches > 1024 || input.MaxEvents <= 0 || input.MaxEvents > 100000 ||
		input.MaxCursorRows <= 0 || input.MaxCursorRows > 100000 || input.MaxShadowRows <= 0 || input.MaxShadowRows > 10000 {
		return AskUserHistoryCutover{}, false, errors.New("complete history cutover authority and bounded resources are required")
	}
	var plan historyCutoverPlan
	created := false
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		backfill, err := r.rebuildAskUserHistoryBackfillConn(ctx, conn, input)
		if err != nil {
			return fmt.Errorf("rebuild history backfill: %w", err)
		}
		plan, err = r.buildAskUserHistoryCutoverPlanConn(ctx, conn, backfill, input)
		if err != nil {
			return fmt.Errorf("build history cutover plan: %w", err)
		}
		found, err := assignHistoryCutoverIdentityConn(ctx, conn, &plan)
		if err != nil {
			return fmt.Errorf("assign history cutover identity: %w", err)
		}
		storedAt, found, err := validateStoredHistoryCutoverConn(ctx, conn, plan)
		if err != nil {
			return fmt.Errorf("validate history cutover retry: %w", err)
		}
		if found {
			plan.Cutover.CreatedAt = storedAt
			return nil
		}
		if err := insertHistoryCutoverConn(ctx, conn, plan); err != nil {
			return fmt.Errorf("persist history cutover plan: %w", err)
		}
		created = true
		return nil
	})
	return plan.Cutover, created, schemaError(err)
}

// ResolveHistoryCutoverCursor resolves only a ready, non-serving v30 plan. It
// deliberately does not activate the plan or reinterpret a stale cursor.
func (r *Repository) ResolveHistoryCutoverCursor(
	ctx context.Context,
	input ResolveHistoryCutoverCursorInput,
) (HistoryCutoverCursorResolution, error) {
	if r == nil || r.db == nil {
		return HistoryCutoverCursorResolution{}, ErrSchemaUnavailable
	}
	input.CutoverID = strings.TrimSpace(input.CutoverID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.SourceBranchID = strings.TrimSpace(input.SourceBranchID)
	if len(input.CutoverID) != sha256.Size*2 || input.OwnerID == "" || !validTranscriptBranchID(input.SourceBranchID) ||
		input.SourceGeneration <= 0 || input.SourceThroughPublicationSequence < 0 || input.SourceMessageIndex < 0 {
		return HistoryCutoverCursorResolution{}, errors.New("complete history cutover cursor authority is required")
	}
	cutoverID, err := hex.DecodeString(input.CutoverID)
	if err != nil {
		return HistoryCutoverCursorResolution{}, ErrEventConflict
	}
	var result HistoryCutoverCursorResolution
	var ownerID, status string
	var activated int
	err = r.db.QueryRowContext(ctx, `SELECT run.owner_id,run.status,run.activated,
		cursor.target_branch_id,cursor.stable_message_id,cursor.target_publication_seq,cursor.target_message_index
		FROM transcript_history_cutover_runs run JOIN transcript_history_cutover_cursor_map cursor
		ON cursor.cutover_id=run.cutover_id
		WHERE run.cutover_id=? AND cursor.source_branch_id=? AND cursor.source_generation=?
		AND cursor.source_through_publication_seq=? AND cursor.source_message_index=?`,
		cutoverID, input.SourceBranchID, input.SourceGeneration, input.SourceThroughPublicationSequence,
		input.SourceMessageIndex,
	).Scan(&ownerID, &status, &activated, &result.TargetBranchID, &result.StableMessageID,
		&result.TargetPublicationSequence, &result.TargetMessageIndex)
	if errors.Is(err, sql.ErrNoRows) {
		var actualOwner string
		var actualGeneration, actualThrough int64
		lookupErr := r.db.QueryRowContext(ctx, `SELECT run.owner_id,cursor.source_generation,cursor.source_through_publication_seq
			FROM transcript_history_cutover_runs run JOIN transcript_history_cutover_cursor_map cursor
			ON cursor.cutover_id=run.cutover_id WHERE run.cutover_id=? AND cursor.source_branch_id=?
			AND cursor.source_message_index=?`, cutoverID, input.SourceBranchID, input.SourceMessageIndex).
			Scan(&actualOwner, &actualGeneration, &actualThrough)
		if lookupErr == nil {
			if actualOwner != input.OwnerID {
				return HistoryCutoverCursorResolution{}, ErrOwnerMismatch
			}
			if actualGeneration != input.SourceGeneration || actualThrough != input.SourceThroughPublicationSequence {
				return HistoryCutoverCursorResolution{}, ErrBranchStateStale
			}
		}
		return HistoryCutoverCursorResolution{}, ErrEventConflict
	}
	if err != nil {
		return HistoryCutoverCursorResolution{}, schemaError(err)
	}
	if ownerID != input.OwnerID {
		return HistoryCutoverCursorResolution{}, ErrOwnerMismatch
	}
	if status != "ready" || activated != 0 || result.TargetBranchID == "" || result.StableMessageID == "" ||
		result.TargetPublicationSequence <= 0 || result.TargetMessageIndex < 0 {
		return HistoryCutoverCursorResolution{}, ErrEventConflict
	}
	return result, nil
}

func (r *Repository) rebuildAskUserHistoryBackfillConn(
	ctx context.Context,
	conn *sql.Conn,
	input PrepareAskUserHistoryCutoverInput,
) (AskUserHistoryBackfill, error) {
	backfillID, err := hex.DecodeString(input.BackfillID)
	if err != nil || len(backfillID) != sha256.Size {
		return AskUserHistoryBackfill{}, ErrEventConflict
	}
	var runID []byte
	var streamUID, branchID string
	var generation int64
	if err := conn.QueryRowContext(ctx, `SELECT classification_run_id,stream_uid,branch_id,branch_generation
		FROM transcript_history_backfill_runs WHERE backfill_id=?`, backfillID).Scan(
		&runID, &streamUID, &branchID, &generation,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AskUserHistoryBackfill{}, ErrHistoryBackfillBlocked
		}
		return AskUserHistoryBackfill{}, err
	}
	if streamUID != input.StreamUID || len(runID) != sha256.Size {
		return AskUserHistoryBackfill{}, ErrEventConflict
	}
	runIDHex := hex.EncodeToString(runID)
	audit, found, err := getAskUserHistoryAuditConn(ctx, conn, GetAskUserHistoryAuditInput{
		RunID: runIDHex, StreamUID: streamUID, OwnerID: input.OwnerID,
	})
	if err != nil || !found {
		if err == nil {
			err = ErrHistoryBackfillBlocked
		}
		return AskUserHistoryBackfill{}, err
	}
	if audit.BranchID != branchID || audit.BranchGeneration != generation {
		return AskUserHistoryBackfill{}, ErrBranchStateStale
	}
	latest, err := latestAskUserHistoryRunIDConn(ctx, conn, streamUID, branchID)
	if err != nil {
		return AskUserHistoryBackfill{}, err
	}
	if latest != runIDHex {
		return AskUserHistoryBackfill{}, ErrBranchStateStale
	}
	snapshot, err := loadAskUserHistorySnapshot(ctx, conn, AuditAskUserHistoryInput{
		StreamUID: streamUID, OwnerID: input.OwnerID, BranchID: branchID,
		MaxEvents: input.MaxEvents, MaxCandidates: input.MaxEvents, MaxShadowRows: input.MaxShadowRows,
	})
	if err != nil {
		return AskUserHistoryBackfill{}, err
	}
	current, err := classifyAskUserHistoryEvents(snapshot.stream, snapshot.branchID, snapshot.generation,
		snapshot.branch, snapshot.events, input.MaxEvents, audit.ClassifiedAt)
	if err != nil {
		return AskUserHistoryBackfill{}, err
	}
	finalizeAskUserHistoryAudit(&current, snapshot)
	if !askUserHistoryAuditsEqual(audit, current) {
		return AskUserHistoryBackfill{}, ErrBranchStateStale
	}
	if err := validateAskUserHistoryBackfillGate(ctx, conn, audit); err != nil {
		return AskUserHistoryBackfill{}, err
	}
	candidates, err := buildAskUserHistoryBackfillCandidates(snapshot, audit, input.MaxEvents)
	if err != nil {
		return AskUserHistoryBackfill{}, err
	}
	backfill, err := newAskUserHistoryBackfill(audit, candidates, r.now().UTC())
	if err != nil || backfill.BackfillID != input.BackfillID {
		return AskUserHistoryBackfill{}, ErrEventConflict
	}
	createdAt, found, err := validateStoredAskUserHistoryBackfillConn(ctx, conn, backfill)
	if err != nil || !found {
		if err == nil {
			err = ErrEventConflict
		}
		return AskUserHistoryBackfill{}, err
	}
	backfill.CreatedAt = createdAt
	return backfill, nil
}

func (r *Repository) buildAskUserHistoryCutoverPlanConn(
	ctx context.Context,
	conn *sql.Conn,
	backfill AskUserHistoryBackfill,
	input PrepareAskUserHistoryCutoverInput,
) (historyCutoverPlan, error) {
	stream, err := getStreamConn(ctx, conn, backfill.StreamUID, backfill.OwnerID)
	if err != nil {
		return historyCutoverPlan{}, err
	}
	var activeBranchID string
	var generation int64
	if err := conn.QueryRowContext(ctx, `SELECT active_branch_id,generation FROM transcript_branch_state WHERE stream_uid=?`,
		stream.UID).Scan(&activeBranchID, &generation); err != nil {
		return historyCutoverPlan{}, err
	}
	if activeBranchID != backfill.BranchID || generation != backfill.BranchGeneration {
		return historyCutoverPlan{}, ErrBranchStateStale
	}
	rebuiltVerification := fullAskUserHistoryBackfillDigest(backfill)
	storedVerification, err := digestStoredAskUserHistoryBackfillRowsConn(ctx, conn, backfill.BackfillID)
	if err != nil {
		return historyCutoverPlan{}, fmt.Errorf("verify stored v29 rows: %w", err)
	}
	verificationHash := sha256.New()
	writeHistoryDigestField(verificationHash, rebuiltVerification[:])
	writeHistoryDigestField(verificationHash, storedVerification[:])
	var verification [sha256.Size]byte
	copy(verification[:], verificationHash.Sum(nil))
	branches, err := loadHistoryCutoverBranchesConn(ctx, conn, stream.UID, input.MaxBranches)
	if err != nil {
		return historyCutoverPlan{}, fmt.Errorf("load branch lineage: %w", err)
	}
	candidateByEvent := map[int64]*AskUserHistoryBackfillCandidate{}
	for index := range backfill.Candidates {
		candidate := &backfill.Candidates[index]
		if candidateByEvent[candidate.ToolEventID] != nil || candidateByEvent[candidate.ResultEventID] != nil {
			return historyCutoverPlan{}, ErrEventConflict
		}
		candidateByEvent[candidate.ToolEventID] = candidate
		candidateByEvent[candidate.ResultEventID] = candidate
	}
	eventsByKey := map[string]historyCutoverEvent{}
	branchSourceEvents := map[string][]historySourceEvent{}
	for branchIndex := range branches {
		sourceEvents, err := loadHistoryCutoverBranchEventsConn(ctx, conn, stream.UID, branches[branchIndex].SourceID, input.MaxEvents)
		if err != nil {
			return historyCutoverPlan{}, fmt.Errorf("load branch events: %w", err)
		}
		branchSourceEvents[branches[branchIndex].SourceID] = sourceEvents
		memberships, targets, err := r.planHistoryCutoverBranchMembershipsConn(
			ctx, conn, backfill, branches[branchIndex], generation, sourceEvents, candidateByEvent, eventsByKey,
		)
		if err != nil {
			return historyCutoverPlan{}, fmt.Errorf("plan branch membership: %w", err)
		}
		branches[branchIndex].Memberships = memberships
		branches[branchIndex].SourceEventTargetKeys = targets
		branches[branchIndex].SourceThroughOrdinal = int64(len(sourceEvents))
		for _, source := range sourceEvents {
			if source.PublicationSeq > branches[branchIndex].SourceThroughPublicationSequence {
				branches[branchIndex].SourceThroughPublicationSequence = source.PublicationSeq
			}
		}
		branches[branchIndex].SourceMembershipSHA256 = digestSourceMembership(sourceEvents)
		branches[branchIndex].TargetMembershipSHA256 = digestTargetMembership(memberships)
	}
	branchesByID := make(map[string]*historyCutoverBranch, len(branches))
	for index := range branches {
		branchesByID[branches[index].SourceID] = &branches[index]
	}
	for index := range branches {
		if branches[index].SourceForkEventID == nil {
			continue
		}
		parent := branchesByID[branches[index].ParentSourceID]
		if parent == nil {
			return historyCutoverPlan{}, ErrEventConflict
		}
		branches[index].TargetForkEventKey = parent.SourceEventTargetKeys[*branches[index].SourceForkEventID]
		if branches[index].TargetForkEventKey == "" {
			return historyCutoverPlan{}, ErrEventConflict
		}
	}
	if len(eventsByKey) == 0 || len(eventsByKey) > input.MaxEvents {
		return historyCutoverPlan{}, ErrHistoryBackfillBlocked
	}
	events := make([]historyCutoverEvent, 0, len(eventsByKey))
	for _, event := range eventsByKey {
		events = append(events, event)
	}
	sort.Slice(events, func(left, right int) bool {
		if events[left].rank != events[right].rank {
			return events[left].rank < events[right].rank
		}
		return events[left].Key < events[right].Key
	})
	eventSequence := make(map[string]int64, len(events))
	for index := range events {
		events[index].TargetPublicationSequence = int64(index + 1)
		eventSequence[events[index].Key] = int64(index + 1)
		eventsByKey[events[index].Key] = events[index]
	}
	receipts, artifacts, err := loadHistoryCutoverTerminalAuthorityConn(ctx, conn, stream, events)
	if err != nil {
		return historyCutoverPlan{}, fmt.Errorf("materialize terminal authority: %w", err)
	}
	cursors, err := buildHistoryCutoverCursorsConn(ctx, conn, stream, backfill.BackfillID, generation, branches, branchSourceEvents,
		candidateByEvent, eventsByKey, receipts, artifacts, input.MaxCursorRows)
	if err != nil {
		return historyCutoverPlan{}, fmt.Errorf("build cursor map: %w", err)
	}
	shadows, err := buildHistoryCutoverShadowsConn(ctx, conn, stream, backfill.BackfillID, generation, branches,
		branchSourceEvents, candidateByEvent, eventsByKey, receipts, artifacts, input.MaxShadowRows, r.now().UTC())
	if err != nil {
		return historyCutoverPlan{}, fmt.Errorf("compare hidden history: %w", err)
	}
	lineage := digestHistoryCutoverLineage(branches, events, receipts, artifacts)
	cursorDigest := digestHistoryCutoverCursors(cursors)
	shadowDigest := digestHistoryCutoverShadows(shadows)
	through := int64(0)
	for _, branch := range branches {
		if branch.SourceID == backfill.BranchID {
			through = branch.SourceThroughPublicationSequence
		}
	}
	now := r.now().UTC()
	cutover := AskUserHistoryCutover{
		BackfillID: backfill.BackfillID, StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: backfill.BranchID, SourceGeneration: generation,
		SourceThroughPublicationSequence: through,
		VerificationSHA256:               hex.EncodeToString(verification[:]), LineageSHA256: hex.EncodeToString(lineage[:]),
		CursorSHA256: hex.EncodeToString(cursorDigest[:]), ShadowSHA256: hex.EncodeToString(shadowDigest[:]),
		BranchCount: len(branches), EventCount: len(events), CursorCount: len(cursors), Ready: true, CreatedAt: now,
	}
	return historyCutoverPlan{
		Cutover: cutover, Events: events, Branches: branches, Cursors: cursors, Shadows: shadows,
		Receipts: receipts, Artifacts: artifacts,
	}, nil
}

func loadHistoryCutoverTerminalAuthorityConn(
	ctx context.Context, conn *sql.Conn, stream Stream, events []historyCutoverEvent,
) ([]historyCutoverReceipt, []historyCutoverArtifactReference, error) {
	targetsBySource := map[int64][]historyCutoverEvent{}
	for _, event := range events {
		if event.SourceEventID != nil {
			targetsBySource[*event.SourceEventID] = append(targetsBySource[*event.SourceEventID], event)
		}
	}
	receipts := []historyCutoverReceipt{}
	rows, err := conn.QueryContext(ctx, `SELECT attempt,event_id,status,finished_at
		FROM transcript_runner_receipts WHERE stream_uid=? ORDER BY attempt`, stream.UID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var attempt, eventID int64
		var status string
		var finishedAt time.Time
		if err := rows.Scan(&attempt, &eventID, &status, &finishedAt); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		targets := targetsBySource[eventID]
		if len(targets) != 1 || targets[0].EventType != "runner_finished" {
			_ = rows.Close()
			return nil, nil, ErrEventConflict
		}
		receipts = append(receipts, historyCutoverReceipt{SourceAttempt: attempt, TargetEventKey: targets[0].Key, Status: status, FinishedAt: finishedAt})
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	artifacts := []historyCutoverArtifactReference{}
	artifactIndex := map[string]int{}
	nextArtifactOrdinal := map[string]int{}
	for _, event := range events {
		if event.SourceEventID == nil {
			continue
		}
		refs, err := artifactReferencesForEventConn(ctx, conn, Event{
			StreamUID: stream.UID, EventID: *event.SourceEventID, Type: event.EventType, RunnerAttempt: event.RunnerAttempt,
		})
		if err != nil {
			return nil, nil, err
		}
		for _, ref := range refs {
			key := event.Key + "\x00" + ref.ArtifactID + "\x00" + ref.VersionID
			if index, found := artifactIndex[key]; found {
				current := artifacts[index]
				if current.Relation != string(ref.Relation) || current.Availability != string(ref.Availability) {
					return nil, nil, ErrEventConflict
				}
				continue
			}
			nextArtifactOrdinal[event.Key]++
			artifacts = append(artifacts, historyCutoverArtifactReference{
				TargetEventKey: event.Key, Ordinal: nextArtifactOrdinal[event.Key], ArtifactID: ref.ArtifactID, VersionID: ref.VersionID,
				Relation: string(ref.Relation), Availability: string(ref.Availability), CreatedAt: ref.CreatedAt,
			})
			artifactIndex[key] = len(artifacts) - 1
		}
	}
	return receipts, artifacts, nil
}

func loadHistoryCutoverBranchesConn(
	ctx context.Context, conn *sql.Conn, streamUID string, limit int,
) ([]historyCutoverBranch, error) {
	rows, err := conn.QueryContext(ctx, `SELECT branch_id,COALESCE(parent_branch_id,''),kind,fork_event_id,
		fork_point,client_mutation_id,request_sha256,source_message_id,created_at,updated_at
		FROM transcript_branches WHERE stream_uid=? ORDER BY created_at,branch_id`, streamUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	branches := make([]historyCutoverBranch, 0, 8)
	sourceTargets := map[string]string{}
	for rows.Next() {
		if len(branches) >= limit {
			return nil, ErrHistoryBackfillBlocked
		}
		var branch historyCutoverBranch
		var forkEventID sql.NullInt64
		var requestDigest []byte
		if err := rows.Scan(&branch.SourceID, &branch.ParentSourceID, &branch.Kind, &forkEventID,
			&branch.ForkPoint, &branch.ClientMutationID, &requestDigest, &branch.SourceMessageID,
			&branch.CreatedAt, &branch.UpdatedAt); err != nil {
			return nil, err
		}
		if forkEventID.Valid {
			value := forkEventID.Int64
			branch.SourceForkEventID = &value
		}
		if len(requestDigest) != sha256.Size {
			return nil, ErrEventConflict
		}
		branch.RequestSHA256 = hex.EncodeToString(requestDigest)
		branch.TargetID = branch.SourceID
		sourceTargets[branch.SourceID] = branch.TargetID
		branches = append(branches, branch)
	}
	if err := rows.Err(); err != nil || len(branches) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, ErrHistoryBackfillBlocked
	}
	for index := range branches {
		if branches[index].ParentSourceID == "" {
			continue
		}
		branches[index].ParentTargetID = sourceTargets[branches[index].ParentSourceID]
		if branches[index].ParentTargetID == "" {
			return nil, fmt.Errorf("parent branch lineage unavailable: %w", ErrEventConflict)
		}
	}
	return branches, nil
}

func loadHistoryCutoverBranchEventsConn(
	ctx context.Context, conn *sql.Conn, streamUID, branchID string, limit int,
) ([]historySourceEvent, error) {
	rows, err := conn.QueryContext(ctx, `SELECT membership.ordinal,event.event_id,event.publication_seq,
		event.client_message_id,event.event_type,event.source,event.runner_attempt,event.payload_json,event.frame_event_id,event.created_at
		FROM transcript_branch_events membership JOIN transcript_events event
		ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		WHERE membership.stream_uid=? AND membership.branch_id=? ORDER BY membership.ordinal`, streamUID, branchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]historySourceEvent, 0, 64)
	for rows.Next() {
		if len(result) >= limit {
			return nil, ErrHistoryBackfillBlocked
		}
		var item historySourceEvent
		var source string
		var attempt sql.NullInt64
		var payload []byte
		var frameEventID sql.NullString
		if err := rows.Scan(&item.Ordinal, &item.EventID, &item.PublicationSeq, &item.ClientMessageID,
			&item.Type, &source, &attempt, &payload, &frameEventID, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.StreamUID = streamUID
		item.Source = EventSource(source)
		item.PayloadJSON = append([]byte(nil), payload...)
		if attempt.Valid {
			value := attempt.Int64
			item.RunnerAttempt = &value
		}
		if frameEventID.Valid {
			value := frameEventID.String
			item.FrameEventID = &value
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		if result[index].Ordinal != int64(index+1) {
			return nil, ErrEventConflict
		}
	}
	return result, nil
}

func (r *Repository) planHistoryCutoverBranchMembershipsConn(
	ctx context.Context,
	conn *sql.Conn,
	backfill AskUserHistoryBackfill,
	branch historyCutoverBranch,
	generation int64,
	sources []historySourceEvent,
	candidates map[int64]*AskUserHistoryBackfillCandidate,
	events map[string]historyCutoverEvent,
) ([]historyCutoverMembership, map[int64]string, error) {
	seenCandidates := map[string]int{}
	nativeOrigins := map[string]AskUserOriginV1{}
	targets := map[int64]string{}
	memberships := make([]historyCutoverMembership, 0, len(sources)+4)
	appendMembership := func(key string, sourceOrdinal *int64) {
		memberships = append(memberships, historyCutoverMembership{
			TargetOrdinal: int64(len(memberships) + 1), EventKey: key, SourceOrdinal: sourceOrdinal,
		})
	}
	for _, source := range sources {
		if candidate := candidates[source.EventID]; candidate != nil {
			seenCandidates[candidate.CandidateID]++
			prompt, pending, terminal, prefix, err := regenerateHistoryCutoverCandidate(backfill, branch, generation, *candidate)
			if err != nil {
				return nil, nil, err
			}
			ordinal := source.Ordinal
			switch source.EventID {
			case candidate.ToolEventID:
				key := prefix + ":prompt"
				if err := addHistoryCutoverEvent(events, historyCutoverEventForAskUser(
					key, "ask_user_prompt", AskUserPromptEventType, prompt.Origin.PromptClientMessageID,
					prompt, candidate.CandidateID, source, 0,
				)); err != nil {
					return nil, nil, err
				}
				appendMembership(key, &ordinal)
				targets[source.EventID] = key
			case candidate.ResultEventID:
				pendingKey := prefix + ":pending"
				if err := addHistoryCutoverEvent(events, historyCutoverEventForAskUser(
					pendingKey, "ask_user_pending", AskUserResultEventType, pending.Origin.PendingClientMessageID,
					pending, candidate.CandidateID, source, 0,
				)); err != nil {
					return nil, nil, err
				}
				appendMembership(pendingKey, &ordinal)
				resultKey := prefix + ":result"
				terminalID, err := AskUserResultClientMessageIDV1(terminal.Origin)
				if err != nil {
					return nil, nil, ErrEventConflict
				}
				if err := addHistoryCutoverEvent(events, historyCutoverEventForAskUser(
					resultKey, "ask_user_result", AskUserResultEventType, terminalID,
					terminal, candidate.CandidateID, source, 1,
				)); err != nil {
					return nil, nil, err
				}
				appendMembership(resultKey, &ordinal)
				targets[source.EventID] = resultKey
			default:
				return nil, nil, ErrEventConflict
			}
			continue
		}
		resolved, err := resolveEventPayloadConn(ctx, conn, source.Event)
		if err != nil {
			return nil, nil, err
		}
		if source.Type == AskUserPromptEventType {
			prompt, err := DecodeAskUserPromptV1(resolved)
			if err != nil {
				return nil, nil, ErrEventConflict
			}
			origin, prefix, err := historyCutoverOrigin(backfill.BackfillID, branch.TargetID, generation, prompt.Origin)
			if err != nil {
				return nil, nil, err
			}
			prompt.Origin = origin
			key := prefix + ":prompt"
			if err := addHistoryCutoverEvent(events, historyCutoverEventForAskUser(
				key, "ask_user_prompt", AskUserPromptEventType, origin.PromptClientMessageID, prompt, "", source, 0,
			)); err != nil {
				return nil, nil, err
			}
			nativeOrigins[prompt.ToolUseID] = origin
			ordinal := source.Ordinal
			appendMembership(key, &ordinal)
			targets[source.EventID] = key
			continue
		}
		if source.Type == AskUserResultEventType {
			result, err := DecodeAskUserResultEventV1(resolved)
			if err != nil {
				return nil, nil, ErrEventConflict
			}
			origin, found := nativeOrigins[result.ToolUseID]
			if !found {
				return nil, nil, ErrEventConflict
			}
			result.Origin = origin
			factKind, suffix, clientID := "ask_user_result", ":result", ""
			if result.Result.Status == AskUserStatusAwaitingResponse {
				factKind, suffix, clientID = "ask_user_pending", ":pending", origin.PendingClientMessageID
			} else {
				clientID, err = AskUserResultClientMessageIDV1(origin)
				if err != nil {
					return nil, nil, ErrEventConflict
				}
			}
			_, prefix, _ := historyCutoverOrigin(backfill.BackfillID, branch.TargetID, generation, origin)
			key := prefix + suffix
			if err := addHistoryCutoverEvent(events, historyCutoverEventForAskUser(
				key, factKind, AskUserResultEventType, clientID, result, "", source, 0,
			)); err != nil {
				return nil, nil, err
			}
			ordinal := source.Ordinal
			appendMembership(key, &ordinal)
			targets[source.EventID] = key
			continue
		}
		key := fmt.Sprintf("event:%d", source.EventID)
		digest := sha256.Sum256(resolved)
		planned := historyCutoverEvent{
			Key: key, FactKind: "existing", SourceEventID: &source.EventID, ClientMessageID: source.ClientMessageID,
			EventType: source.Type, RunnerAttempt: source.RunnerAttempt, PayloadJSON: resolved,
			PayloadSHA256: hex.EncodeToString(digest[:]), CreatedAt: source.CreatedAt, rank: source.PublicationSeq * 4,
		}
		if existing, found := events[key]; found && !historyCutoverEventsEqual(existing, planned) {
			return nil, nil, ErrEventConflict
		}
		events[key] = planned
		ordinal := source.Ordinal
		appendMembership(key, &ordinal)
		targets[source.EventID] = key
	}
	for candidateID, count := range seenCandidates {
		if count != 2 || candidateID == "" {
			return nil, nil, ErrHistoryBackfillBlocked
		}
	}
	return memberships, targets, nil
}

func addHistoryCutoverEvent(events map[string]historyCutoverEvent, planned historyCutoverEvent) error {
	if existing, found := events[planned.Key]; found && !historyCutoverEventsEqual(existing, planned) {
		return ErrEventConflict
	}
	events[planned.Key] = planned
	return nil
}

func historyCutoverEventForAskUser(
	key, factKind, eventType, clientMessageID string,
	payload any,
	candidateID string,
	source historySourceEvent,
	rankOffset int64,
) historyCutoverEvent {
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return historyCutoverEvent{
		Key: key, FactKind: factKind, SourceEventID: &source.EventID, CandidateID: candidateID,
		ClientMessageID: clientMessageID, EventType: eventType, RunnerAttempt: source.RunnerAttempt,
		PayloadJSON: encoded, PayloadSHA256: hex.EncodeToString(digest[:]), CreatedAt: source.CreatedAt,
		rank: source.PublicationSeq*4 + rankOffset,
	}
}

func regenerateHistoryCutoverCandidate(
	backfill AskUserHistoryBackfill,
	branch historyCutoverBranch,
	generation int64,
	candidate AskUserHistoryBackfillCandidate,
) (AskUserPromptV1, AskUserResultEventV1, AskUserResultEventV1, string, error) {
	prompt, err := DecodeAskUserPromptV1(candidate.PromptJSON)
	if err != nil {
		return AskUserPromptV1{}, AskUserResultEventV1{}, AskUserResultEventV1{}, "", ErrEventConflict
	}
	pending, err := DecodeAskUserResultEventV1(candidate.PendingJSON)
	if err != nil {
		return AskUserPromptV1{}, AskUserResultEventV1{}, AskUserResultEventV1{}, "", ErrEventConflict
	}
	terminal, err := DecodeAskUserResultEventV1(candidate.ResultJSON)
	if err != nil {
		return AskUserPromptV1{}, AskUserResultEventV1{}, AskUserResultEventV1{}, "", ErrEventConflict
	}
	origin, prefix, err := historyCutoverOrigin(backfill.BackfillID, branch.TargetID, generation, prompt.Origin)
	if err != nil {
		return AskUserPromptV1{}, AskUserResultEventV1{}, AskUserResultEventV1{}, "", err
	}
	prompt.Origin, pending.Origin, terminal.Origin = origin, origin, origin
	return prompt, pending, terminal, prefix, nil
}

func historyCutoverOrigin(
	backfillID, branchID string,
	generation int64,
	origin AskUserOriginV1,
) (AskUserOriginV1, string, error) {
	if !validTranscriptBranchID(branchID) || generation <= 0 || strings.TrimSpace(origin.ToolUseID) == "" {
		return AskUserOriginV1{}, "", ErrEventConflict
	}
	digest := sha256.Sum256([]byte(HistoryCutoverContractID + "\x00" + backfillID + "\x00" + branchID + "\x00" + origin.ToolUseID))
	prefix := "history-cutover:" + hex.EncodeToString(digest[:16])
	origin.BranchID = branchID
	origin.BranchGeneration = generation
	origin.PromptClientMessageID = prefix + ":prompt"
	origin.PendingClientMessageID = prefix + ":pending"
	return origin, "ask:" + branchID + ":" + hex.EncodeToString(digest[:16]), nil
}

func historyCutoverEventsEqual(left, right historyCutoverEvent) bool {
	return left.Key == right.Key && left.FactKind == right.FactKind && left.ClientMessageID == right.ClientMessageID &&
		left.EventType == right.EventType && left.CandidateID == right.CandidateID && left.PayloadSHA256 == right.PayloadSHA256 &&
		left.rank == right.rank && historyCutoverNullableInt64Equal(left.SourceEventID, right.SourceEventID) &&
		historyCutoverNullableInt64Equal(left.RunnerAttempt, right.RunnerAttempt)
}

func historyCutoverNullableInt64Equal(left, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

type historyCutoverProjectedMessage struct {
	SemanticKey, StableID, AskUserState, TerminalState string
	SourceOrdinal, TargetPublicationSequence           int64
	Artifacts                                          []string
}

func buildHistoryCutoverCursorsConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	backfillID string,
	generation int64,
	branches []historyCutoverBranch,
	sourceEvents map[string][]historySourceEvent,
	candidates map[int64]*AskUserHistoryBackfillCandidate,
	events map[string]historyCutoverEvent,
	receipts []historyCutoverReceipt,
	artifacts []historyCutoverArtifactReference,
	limit int,
) ([]historyCutoverCursor, error) {
	result := make([]historyCutoverCursor, 0, 64)
	for _, branch := range branches {
		source, err := projectHistoryCutoverSourceConn(ctx, conn, stream, backfillID, generation, branch,
			sourceEvents[branch.SourceID], candidates)
		if err != nil {
			return nil, err
		}
		target, err := projectHistoryCutoverTarget(stream, generation, branch, events, receipts, artifacts)
		if err != nil {
			return nil, err
		}
		if len(source) != len(target) {
			return nil, ErrHistoryBackfillBlocked
		}
		for index := range source {
			if source[index].SemanticKey != target[index].SemanticKey {
				return nil, ErrHistoryBackfillBlocked
			}
			result = append(result, historyCutoverCursor{
				SourceBranchID: branch.SourceID, TargetBranchID: branch.TargetID,
				SourceGeneration: generation, SourceThroughPublicationSequence: branch.SourceThroughPublicationSequence,
				SourceMessageIndex: int64(index), SourceOrdinal: source[index].SourceOrdinal,
				StableMessageID:           target[index].StableID,
				TargetPublicationSequence: target[index].TargetPublicationSequence,
				TargetMessageIndex:        int64(index),
			})
			if len(result) > limit {
				return nil, ErrHistoryBackfillBlocked
			}
		}
	}
	if len(result) == 0 {
		return nil, ErrHistoryBackfillBlocked
	}
	return result, nil
}

func buildHistoryCutoverShadowsConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	backfillID string,
	generation int64,
	branches []historyCutoverBranch,
	sources map[string][]historySourceEvent,
	candidates map[int64]*AskUserHistoryBackfillCandidate,
	events map[string]historyCutoverEvent,
	receipts []historyCutoverReceipt,
	artifacts []historyCutoverArtifactReference,
	limit int,
	_ time.Time,
) ([]historyCutoverShadow, error) {
	result := make([]historyCutoverShadow, 0, len(branches)*len(askUserHistoryShadowDimensions))
	for _, branch := range branches {
		if len(result)+len(askUserHistoryShadowDimensions) > limit {
			return nil, ErrHistoryBackfillBlocked
		}
		legacy, err := projectHistoryCutoverSourceConn(ctx, conn, stream, backfillID, generation, branch,
			sources[branch.SourceID], candidates)
		if err != nil {
			return nil, err
		}
		target, err := projectHistoryCutoverTarget(stream, generation, branch, events, receipts, artifacts)
		if err != nil {
			return nil, err
		}
		legacyDigests := digestHistoryCutoverProjection(legacy)
		targetDigests := digestHistoryCutoverProjection(target)
		for _, dimension := range askUserHistoryShadowDimensions {
			if legacyDigests[dimension] != targetDigests[dimension] {
				return nil, fmt.Errorf("%w: %s projection mismatch", ErrHistoryBackfillBlocked, dimension)
			}
			result = append(result, historyCutoverShadow{
				SourceBranchID: branch.SourceID, Dimension: string(dimension), Digest: legacyDigests[dimension],
			})
		}
	}
	return result, nil
}

func projectHistoryCutoverSourceConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	backfillID string,
	generation int64,
	branch historyCutoverBranch,
	sources []historySourceEvent,
	candidates map[int64]*AskUserHistoryBackfillCandidate,
) ([]historyCutoverProjectedMessage, error) {
	messages := []historyCutoverProjectedMessage{}
	assistant := map[int64]int{}
	askUser := map[string]int{}
	for _, source := range sources {
		messageIndex := -1
		if candidate := candidates[source.EventID]; candidate != nil {
			prompt, _, terminal, _, err := regenerateHistoryCutoverCandidate(
				AskUserHistoryBackfill{BackfillID: backfillID}, branch, generation, *candidate,
			)
			if err != nil {
				return nil, err
			}
			if source.EventID == candidate.ToolEventID {
				if _, duplicate := askUser[candidate.ToolUseID]; duplicate {
					return nil, ErrEventConflict
				}
				messages = append(messages, historyCutoverProjectedMessage{
					SemanticKey: "ask:" + candidate.ToolUseID, StableID: prompt.Origin.PromptClientMessageID,
					AskUserState: historyCutoverAskUserState(prompt, nil), SourceOrdinal: source.Ordinal,
				})
				askUser[candidate.ToolUseID] = len(messages) - 1
				messageIndex = len(messages) - 1
			} else {
				index, found := askUser[candidate.ToolUseID]
				if !found {
					return nil, ErrEventConflict
				}
				messages[index].AskUserState = historyCutoverAskUserState(prompt, &terminal)
				messageIndex = index
			}
		} else {
			resolved, err := resolveEventPayloadConn(ctx, conn, source.Event)
			if err != nil {
				return nil, err
			}
			switch source.Type {
			case AskUserPromptEventType:
				prompt, err := DecodeAskUserPromptV1(resolved)
				if err != nil {
					return nil, ErrEventConflict
				}
				origin, _, err := historyCutoverOrigin(backfillID, branch.TargetID, generation, prompt.Origin)
				if err != nil {
					return nil, err
				}
				prompt.Origin = origin
				if _, duplicate := askUser[prompt.ToolUseID]; duplicate {
					return nil, ErrEventConflict
				}
				messages = append(messages, historyCutoverProjectedMessage{
					SemanticKey: "ask:" + prompt.ToolUseID, StableID: origin.PromptClientMessageID,
					AskUserState: historyCutoverAskUserState(prompt, nil), SourceOrdinal: source.Ordinal,
				})
				askUser[prompt.ToolUseID] = len(messages) - 1
				messageIndex = len(messages) - 1
			case AskUserResultEventType:
				result, err := DecodeAskUserResultEventV1(resolved)
				if err != nil {
					return nil, ErrEventConflict
				}
				index, found := askUser[result.ToolUseID]
				if !found {
					return nil, ErrEventConflict
				}
				promptState := messages[index].AskUserState
				messages[index].AskUserState = historyCutoverAskUserStateFromPrior(promptState, result)
				messageIndex = index
			case "user_input_response":
				continue
			case "user_message":
				messages = append(messages, historyCutoverProjectedMessage{
					SemanticKey: "user:" + source.ClientMessageID, StableID: source.ClientMessageID, SourceOrdinal: source.Ordinal,
				})
				messageIndex = len(messages) - 1
			case "content_delta", "content_reset", "assistant_message":
				if source.RunnerAttempt != nil {
					index, found := assistant[*source.RunnerAttempt]
					if !found {
						messages = append(messages, historyCutoverProjectedMessage{
							SemanticKey:   fmt.Sprintf("assistant:%d", *source.RunnerAttempt),
							StableID:      fmt.Sprintf("assistant-%s-%d", stream.SessionID, *source.RunnerAttempt),
							SourceOrdinal: source.Ordinal,
						})
						index = len(messages) - 1
						assistant[*source.RunnerAttempt] = index
					}
					messageIndex = index
				} else {
					messages = append(messages, historyCutoverProjectedMessage{
						SemanticKey: "assistant-frame:" + source.ClientMessageID, StableID: source.ClientMessageID,
						SourceOrdinal: source.Ordinal,
					})
					messageIndex = len(messages) - 1
				}
			case "runner_finished":
				if source.RunnerAttempt == nil {
					return nil, ErrEventConflict
				}
				index, found := assistant[*source.RunnerAttempt]
				if !found {
					messages = append(messages, historyCutoverProjectedMessage{
						SemanticKey:   fmt.Sprintf("assistant:%d", *source.RunnerAttempt),
						StableID:      fmt.Sprintf("assistant-%s-%d", stream.SessionID, *source.RunnerAttempt),
						SourceOrdinal: source.Ordinal,
					})
					index = len(messages) - 1
					assistant[*source.RunnerAttempt] = index
				}
				var status string
				var finishedAt time.Time
				if err := conn.QueryRowContext(ctx, `SELECT status,finished_at FROM transcript_runner_receipts
					WHERE stream_uid=? AND attempt=? AND event_id=?`, stream.UID, *source.RunnerAttempt, source.EventID).
					Scan(&status, &finishedAt); err != nil {
					return nil, err
				}
				payloadDigest := sha256.Sum256(resolved)
				messages[index].TerminalState = status + "\x00" + finishedAt.UTC().Format(time.RFC3339Nano) + "\x00" + hex.EncodeToString(payloadDigest[:])
				messageIndex = index
			}
		}
		if messageIndex >= 0 {
			refs, err := artifactReferencesForEventConn(ctx, conn, source.Event)
			if err != nil {
				return nil, err
			}
			for _, ref := range refs {
				messages[messageIndex].Artifacts = appendHistoryCutoverArtifact(messages[messageIndex].Artifacts,
					fmt.Sprintf("%s\x00%s\x00%s\x00%s", ref.ArtifactID, ref.VersionID, ref.Relation, ref.Availability))
			}
		}
	}
	return messages, nil
}

func projectHistoryCutoverTarget(
	stream Stream,
	generation int64,
	branch historyCutoverBranch,
	events map[string]historyCutoverEvent,
	receipts []historyCutoverReceipt,
	artifacts []historyCutoverArtifactReference,
) ([]historyCutoverProjectedMessage, error) {
	receiptByEvent := map[string]historyCutoverReceipt{}
	for _, receipt := range receipts {
		receiptByEvent[receipt.TargetEventKey] = receipt
	}
	artifactsByEvent := map[string][]historyCutoverArtifactReference{}
	for _, ref := range artifacts {
		artifactsByEvent[ref.TargetEventKey] = append(artifactsByEvent[ref.TargetEventKey], ref)
	}
	messages := []historyCutoverProjectedMessage{}
	assistant := map[int64]int{}
	askUser := map[string]int{}
	for _, membership := range branch.Memberships {
		event, found := events[membership.EventKey]
		if !found || event.TargetPublicationSequence <= 0 {
			return nil, ErrEventConflict
		}
		messageIndex := -1
		switch event.EventType {
		case AskUserPromptEventType:
			prompt, err := DecodeAskUserPromptV1(event.PayloadJSON)
			if err != nil || prompt.Origin.BranchID != branch.TargetID || prompt.Origin.BranchGeneration != generation ||
				prompt.Origin.PromptClientMessageID != event.ClientMessageID {
				return nil, ErrEventConflict
			}
			if _, duplicate := askUser[prompt.ToolUseID]; duplicate {
				return nil, ErrEventConflict
			}
			messages = append(messages, historyCutoverProjectedMessage{
				SemanticKey: "ask:" + prompt.ToolUseID, StableID: event.ClientMessageID,
				AskUserState:  historyCutoverAskUserState(prompt, nil),
				SourceOrdinal: cutoverSourceOrdinal(membership), TargetPublicationSequence: event.TargetPublicationSequence,
			})
			askUser[prompt.ToolUseID] = len(messages) - 1
			messageIndex = len(messages) - 1
		case AskUserResultEventType:
			result, err := DecodeAskUserResultEventV1(event.PayloadJSON)
			if err != nil || result.Origin.BranchID != branch.TargetID || result.Origin.BranchGeneration != generation {
				return nil, ErrEventConflict
			}
			index, found := askUser[result.ToolUseID]
			if !found {
				return nil, ErrEventConflict
			}
			if result.Result.Status == AskUserStatusAwaitingResponse {
				if event.ClientMessageID != result.Origin.PendingClientMessageID {
					return nil, ErrEventConflict
				}
			} else {
				expected, err := AskUserResultClientMessageIDV1(result.Origin)
				if err != nil || expected != event.ClientMessageID {
					return nil, ErrEventConflict
				}
			}
			messages[index].AskUserState = historyCutoverAskUserStateFromPrior(messages[index].AskUserState, result)
			messageIndex = index
		case "user_input_response":
			continue
		case "user_message":
			messages = append(messages, historyCutoverProjectedMessage{
				SemanticKey: "user:" + event.ClientMessageID, StableID: event.ClientMessageID,
				SourceOrdinal: cutoverSourceOrdinal(membership), TargetPublicationSequence: event.TargetPublicationSequence,
			})
			messageIndex = len(messages) - 1
		case "content_delta", "content_reset", "assistant_message":
			if event.RunnerAttempt != nil {
				index, found := assistant[*event.RunnerAttempt]
				if !found {
					messages = append(messages, historyCutoverProjectedMessage{
						SemanticKey:   fmt.Sprintf("assistant:%d", *event.RunnerAttempt),
						StableID:      fmt.Sprintf("assistant-%s-%d", stream.SessionID, *event.RunnerAttempt),
						SourceOrdinal: cutoverSourceOrdinal(membership), TargetPublicationSequence: event.TargetPublicationSequence,
					})
					index = len(messages) - 1
					assistant[*event.RunnerAttempt] = index
				}
				messageIndex = index
			} else {
				messages = append(messages, historyCutoverProjectedMessage{
					SemanticKey: "assistant-frame:" + event.ClientMessageID, StableID: event.ClientMessageID,
					SourceOrdinal: cutoverSourceOrdinal(membership), TargetPublicationSequence: event.TargetPublicationSequence,
				})
				messageIndex = len(messages) - 1
			}
		case "runner_finished":
			if event.RunnerAttempt == nil {
				return nil, ErrEventConflict
			}
			index, found := assistant[*event.RunnerAttempt]
			if !found {
				messages = append(messages, historyCutoverProjectedMessage{
					SemanticKey:   fmt.Sprintf("assistant:%d", *event.RunnerAttempt),
					StableID:      fmt.Sprintf("assistant-%s-%d", stream.SessionID, *event.RunnerAttempt),
					SourceOrdinal: cutoverSourceOrdinal(membership), TargetPublicationSequence: event.TargetPublicationSequence,
				})
				index = len(messages) - 1
				assistant[*event.RunnerAttempt] = index
			}
			receipt, found := receiptByEvent[event.Key]
			if !found || receipt.SourceAttempt != *event.RunnerAttempt {
				return nil, ErrEventConflict
			}
			messages[index].TerminalState = receipt.Status + "\x00" + receipt.FinishedAt.UTC().Format(time.RFC3339Nano) + "\x00" + event.PayloadSHA256
			messageIndex = index
		}
		if messageIndex >= 0 {
			for _, ref := range artifactsByEvent[event.Key] {
				messages[messageIndex].Artifacts = appendHistoryCutoverArtifact(messages[messageIndex].Artifacts,
					fmt.Sprintf("%s\x00%s\x00%s\x00%s", ref.ArtifactID, ref.VersionID, ref.Relation, ref.Availability))
			}
		}
	}
	return messages, nil
}

func appendHistoryCutoverArtifact(existing []string, value string) []string {
	for _, current := range existing {
		if current == value {
			return existing
		}
	}
	return append(existing, value)
}

func cutoverSourceOrdinal(membership historyCutoverMembership) int64 {
	if membership.SourceOrdinal == nil {
		return membership.TargetOrdinal
	}
	return *membership.SourceOrdinal
}

func historyCutoverAskUserState(prompt AskUserPromptV1, result *AskUserResultEventV1) string {
	questions, _ := json.Marshal(prompt.Questions)
	state := prompt.ToolUseID + "\x00" + string(questions)
	if result != nil {
		return historyCutoverAskUserStateFromPrior(state, *result)
	}
	return state
}

func historyCutoverAskUserStateFromPrior(prior string, result AskUserResultEventV1) string {
	if separator := strings.IndexByte(prior, 0); separator >= 0 {
		// Keep the immutable prompt portion and replace only the latest result.
		questionsEnd := strings.LastIndex(prior, "\x00result:")
		if questionsEnd >= 0 {
			prior = prior[:questionsEnd]
		}
	}
	encoded, _ := json.Marshal(result.Result)
	return prior + "\x00result:" + string(encoded) + "\x00" + result.ModelContinuation
}

func digestHistoryCutoverProjection(messages []historyCutoverProjectedMessage) map[AskUserHistoryShadowDimension]string {
	digests := map[AskUserHistoryShadowDimension]hash.Hash{
		AskUserHistoryShadowStableIDs: sha256.New(), AskUserHistoryShadowBranchOrder: sha256.New(),
		AskUserHistoryShadowStates: sha256.New(), AskUserHistoryShadowTerminal: sha256.New(),
		AskUserHistoryShadowArtifacts: sha256.New(),
	}
	for _, message := range messages {
		writeHistoryDigestField(digests[AskUserHistoryShadowStableIDs], []byte(message.StableID))
		writeHistoryDigestField(digests[AskUserHistoryShadowBranchOrder], []byte(message.SemanticKey))
		if message.AskUserState != "" {
			writeHistoryDigestField(digests[AskUserHistoryShadowStates], []byte(message.AskUserState))
		}
		if message.TerminalState != "" {
			writeHistoryDigestField(digests[AskUserHistoryShadowTerminal], []byte(message.TerminalState))
		}
		sort.Strings(message.Artifacts)
		for _, ref := range message.Artifacts {
			writeHistoryDigestField(digests[AskUserHistoryShadowArtifacts], []byte(message.SemanticKey+"\x00"+ref))
		}
	}
	result := map[AskUserHistoryShadowDimension]string{}
	for dimension, digest := range digests {
		result[dimension] = hex.EncodeToString(digest.Sum(nil))
	}
	return result
}

func fullAskUserHistoryBackfillDigest(backfill AskUserHistoryBackfill) [sha256.Size]byte {
	digest := sha256.New()
	for _, value := range []string{
		HistoryBackfillContractID, backfill.BackfillID, backfill.ClassificationRunID, backfill.StreamUID,
		backfill.OwnerID, backfill.BranchID, fmt.Sprint(backfill.BranchGeneration), fmt.Sprint(backfill.ContractVersion),
		backfill.SourceSHA256, backfill.StagingSHA256,
		backfill.CreatedAt.UTC().Format(time.RFC3339Nano),
	} {
		writeHistoryDigestField(digest, []byte(value))
	}
	for _, candidate := range backfill.Candidates {
		for _, value := range []string{
			candidate.CandidateID, fmt.Sprint(candidate.CandidateOrdinal), candidate.ToolUseID,
			fmt.Sprint(candidate.RunnerAttempt), fmt.Sprint(candidate.ToolEventID), fmt.Sprint(candidate.ResultEventID),
			candidate.ToolFrameEventID, candidate.ResultFrameEventID, fmt.Sprint(candidate.ToolOrdinal),
			fmt.Sprint(candidate.ResultOrdinal), candidate.PromptClientMessageID, candidate.PendingClientMessageID,
			candidate.TerminalClientMessageID, candidate.StableMessageID, candidate.PayloadSHA256, candidate.EvidenceSHA256,
		} {
			writeHistoryDigestField(digest, []byte(value))
		}
		writeHistoryDigestField(digest, candidate.PromptJSON)
		writeHistoryDigestField(digest, candidate.PendingJSON)
		writeHistoryDigestField(digest, candidate.ResultJSON)
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func digestStoredAskUserHistoryBackfillRowsConn(
	ctx context.Context, conn *sql.Conn, backfillIDHex string,
) ([sha256.Size]byte, error) {
	backfillID, err := hex.DecodeString(backfillIDHex)
	if err != nil || len(backfillID) != sha256.Size {
		return [sha256.Size]byte{}, ErrEventConflict
	}
	digest := sha256.New()
	var storedBackfillID, runID, sourceDigest, stagingDigest []byte
	var streamUID, branchID string
	var generation int64
	var contractVersion, candidateCount int
	var createdAt time.Time
	if err := conn.QueryRowContext(ctx, `SELECT backfill_id,classification_run_id,stream_uid,branch_id,
		branch_generation,contract_version,source_sha256,staging_sha256,candidate_count,created_at
		FROM transcript_history_backfill_runs WHERE backfill_id=?`, backfillID).Scan(
		&storedBackfillID, &runID, &streamUID, &branchID, &generation, &contractVersion,
		&sourceDigest, &stagingDigest, &candidateCount, &createdAt,
	); err != nil {
		return [sha256.Size]byte{}, err
	}
	for _, value := range []string{hex.EncodeToString(storedBackfillID), hex.EncodeToString(runID), streamUID, branchID,
		fmt.Sprint(generation), fmt.Sprint(contractVersion), hex.EncodeToString(sourceDigest), hex.EncodeToString(stagingDigest),
		fmt.Sprint(candidateCount), createdAt.UTC().Format(time.RFC3339Nano)} {
		writeHistoryDigestField(digest, []byte(value))
	}
	rows, err := conn.QueryContext(ctx, `SELECT candidate_id,candidate_ordinal,tool_use_id,runner_attempt,
		tool_event_id,result_event_id,tool_frame_event_id,result_frame_event_id,prompt_client_message_id,
		pending_client_message_id,terminal_client_message_id,prompt_json,pending_json,result_json,payload_sha256,
		evidence_sha256,created_at FROM transcript_history_backfill_candidates
		WHERE backfill_id=? ORDER BY candidate_ordinal`, backfillID)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	seenCandidates := 0
	for rows.Next() {
		var candidateID, promptJSON, pendingJSON, resultJSON, payloadDigest, evidenceDigest []byte
		var ordinal int
		var toolUseID, toolFrameID, resultFrameID, promptID, pendingID, terminalID string
		var attempt, toolEventID, resultEventID int64
		var rowCreatedAt time.Time
		if err := rows.Scan(&candidateID, &ordinal, &toolUseID, &attempt, &toolEventID, &resultEventID,
			&toolFrameID, &resultFrameID, &promptID, &pendingID, &terminalID, &promptJSON, &pendingJSON,
			&resultJSON, &payloadDigest, &evidenceDigest, &rowCreatedAt); err != nil {
			_ = rows.Close()
			return [sha256.Size]byte{}, err
		}
		if rowCreatedAt != createdAt || ordinal != seenCandidates+1 {
			_ = rows.Close()
			return [sha256.Size]byte{}, ErrEventConflict
		}
		for _, value := range []string{hex.EncodeToString(candidateID), fmt.Sprint(ordinal), toolUseID,
			fmt.Sprint(attempt), fmt.Sprint(toolEventID), fmt.Sprint(resultEventID), toolFrameID, resultFrameID,
			promptID, pendingID, terminalID, hex.EncodeToString(payloadDigest), hex.EncodeToString(evidenceDigest),
			rowCreatedAt.UTC().Format(time.RFC3339Nano)} {
			writeHistoryDigestField(digest, []byte(value))
		}
		writeHistoryDigestField(digest, promptJSON)
		writeHistoryDigestField(digest, pendingJSON)
		writeHistoryDigestField(digest, resultJSON)
		seenCandidates++
	}
	if err := rows.Close(); err != nil {
		return [sha256.Size]byte{}, err
	}
	if seenCandidates != candidateCount {
		return [sha256.Size]byte{}, ErrEventConflict
	}
	cursorRows, err := conn.QueryContext(ctx, `SELECT candidate_id,legacy_ordinal,stable_message_id,created_at
		FROM transcript_history_backfill_cursor_map WHERE backfill_id=? ORDER BY legacy_ordinal`, backfillID)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	seenCursors := 0
	for cursorRows.Next() {
		var candidateID []byte
		var ordinal int64
		var stableID string
		var rowCreatedAt time.Time
		if err := cursorRows.Scan(&candidateID, &ordinal, &stableID, &rowCreatedAt); err != nil {
			_ = cursorRows.Close()
			return [sha256.Size]byte{}, err
		}
		if rowCreatedAt != createdAt {
			_ = cursorRows.Close()
			return [sha256.Size]byte{}, ErrEventConflict
		}
		for _, value := range []string{hex.EncodeToString(candidateID), fmt.Sprint(ordinal), stableID,
			rowCreatedAt.UTC().Format(time.RFC3339Nano)} {
			writeHistoryDigestField(digest, []byte(value))
		}
		seenCursors++
	}
	if err := cursorRows.Close(); err != nil {
		return [sha256.Size]byte{}, err
	}
	if seenCursors != candidateCount*2 {
		return [sha256.Size]byte{}, ErrEventConflict
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result, nil
}

func digestSourceMembership(events []historySourceEvent) string {
	digest := sha256.New()
	for _, event := range events {
		writeHistoryDigestField(digest, []byte(fmt.Sprintf("%d\x00%d\x00%d", event.Ordinal, event.EventID, event.PublicationSeq)))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func digestTargetMembership(events []historyCutoverMembership) string {
	digest := sha256.New()
	for _, event := range events {
		sourceOrdinal := ""
		if event.SourceOrdinal != nil {
			sourceOrdinal = fmt.Sprint(*event.SourceOrdinal)
		}
		writeHistoryDigestField(digest, []byte(fmt.Sprintf("%d\x00%s\x00%s", event.TargetOrdinal, event.EventKey, sourceOrdinal)))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func digestHistoryCutoverLineage(
	branches []historyCutoverBranch,
	events []historyCutoverEvent,
	receipts []historyCutoverReceipt,
	artifacts []historyCutoverArtifactReference,
) [sha256.Size]byte {
	orderedBranches := append([]historyCutoverBranch(nil), branches...)
	sort.Slice(orderedBranches, func(left, right int) bool { return orderedBranches[left].SourceID < orderedBranches[right].SourceID })
	digest := sha256.New()
	for _, branch := range orderedBranches {
		forkEventID := ""
		if branch.SourceForkEventID != nil {
			forkEventID = fmt.Sprint(*branch.SourceForkEventID)
		}
		for _, value := range []string{branch.SourceID, branch.TargetID, branch.ParentSourceID, branch.ParentTargetID,
			branch.Kind, forkEventID, branch.TargetForkEventKey, fmt.Sprint(branch.ForkPoint), branch.ClientMutationID,
			branch.RequestSHA256, branch.SourceMessageID, fmt.Sprint(branch.SourceThroughOrdinal),
			fmt.Sprint(branch.SourceThroughPublicationSequence), branch.SourceMembershipSHA256,
			branch.TargetMembershipSHA256, branch.CreatedAt.UTC().Format(time.RFC3339Nano),
			branch.UpdatedAt.UTC().Format(time.RFC3339Nano)} {
			writeHistoryDigestField(digest, []byte(value))
		}
	}
	for _, receipt := range receipts {
		for _, value := range []string{fmt.Sprint(receipt.SourceAttempt), receipt.TargetEventKey, receipt.Status,
			receipt.FinishedAt.UTC().Format(time.RFC3339Nano)} {
			writeHistoryDigestField(digest, []byte(value))
		}
	}
	orderedArtifacts := append([]historyCutoverArtifactReference(nil), artifacts...)
	sort.Slice(orderedArtifacts, func(left, right int) bool {
		if orderedArtifacts[left].TargetEventKey != orderedArtifacts[right].TargetEventKey {
			return orderedArtifacts[left].TargetEventKey < orderedArtifacts[right].TargetEventKey
		}
		return orderedArtifacts[left].Ordinal < orderedArtifacts[right].Ordinal
	})
	for _, ref := range orderedArtifacts {
		for _, value := range []string{ref.TargetEventKey, fmt.Sprint(ref.Ordinal), ref.ArtifactID, ref.VersionID,
			ref.Relation, ref.Availability, ref.CreatedAt.UTC().Format(time.RFC3339Nano)} {
			writeHistoryDigestField(digest, []byte(value))
		}
	}
	for _, event := range events {
		sourceEventID, candidateID, runnerAttempt := "", event.CandidateID, ""
		if event.SourceEventID != nil {
			sourceEventID = fmt.Sprint(*event.SourceEventID)
		}
		if event.RunnerAttempt != nil {
			runnerAttempt = fmt.Sprint(*event.RunnerAttempt)
		}
		for _, value := range []string{event.Key, event.FactKind, sourceEventID, candidateID,
			event.ClientMessageID, event.EventType, runnerAttempt, fmt.Sprint(event.TargetPublicationSequence),
			event.PayloadSHA256, event.CreatedAt.UTC().Format(time.RFC3339Nano)} {
			writeHistoryDigestField(digest, []byte(value))
		}
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func digestHistoryCutoverCursors(cursors []historyCutoverCursor) [sha256.Size]byte {
	ordered := append([]historyCutoverCursor(nil), cursors...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].SourceBranchID != ordered[right].SourceBranchID {
			return ordered[left].SourceBranchID < ordered[right].SourceBranchID
		}
		return ordered[left].SourceMessageIndex < ordered[right].SourceMessageIndex
	})
	digest := sha256.New()
	for _, cursor := range ordered {
		for _, value := range []string{cursor.SourceBranchID, cursor.TargetBranchID, fmt.Sprint(cursor.SourceGeneration),
			fmt.Sprint(cursor.SourceThroughPublicationSequence), fmt.Sprint(cursor.SourceMessageIndex),
			fmt.Sprint(cursor.SourceOrdinal), cursor.StableMessageID, fmt.Sprint(cursor.TargetPublicationSequence),
			fmt.Sprint(cursor.TargetMessageIndex)} {
			writeHistoryDigestField(digest, []byte(value))
		}
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func digestHistoryCutoverShadows(shadows []historyCutoverShadow) [sha256.Size]byte {
	ordered := append([]historyCutoverShadow(nil), shadows...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].SourceBranchID != ordered[right].SourceBranchID {
			return ordered[left].SourceBranchID < ordered[right].SourceBranchID
		}
		return historyCutoverShadowRank(ordered[left].Dimension) < historyCutoverShadowRank(ordered[right].Dimension)
	})
	digest := sha256.New()
	for _, shadow := range ordered {
		writeHistoryDigestField(digest, []byte(shadow.SourceBranchID))
		writeHistoryDigestField(digest, []byte(shadow.Dimension))
		writeHistoryDigestField(digest, []byte(shadow.Digest))
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func assignHistoryCutoverIdentityConn(
	ctx context.Context, conn *sql.Conn, plan *historyCutoverPlan,
) (bool, error) {
	backfillID, err := hex.DecodeString(plan.Cutover.BackfillID)
	if err != nil || len(backfillID) != sha256.Size {
		return false, ErrEventConflict
	}
	var existingID, verification, lineage, cursor, shadow []byte
	err = conn.QueryRowContext(ctx, `SELECT cutover_id,verification_sha256,lineage_sha256,cursor_sha256,shadow_sha256
		FROM transcript_history_cutover_runs WHERE backfill_id=? AND status='ready'
		ORDER BY updated_at DESC,cutover_id DESC LIMIT 1`, backfillID).Scan(
		&existingID, &verification, &lineage, &cursor, &shadow,
	)
	predecessor := ""
	if err == nil {
		predecessor = hex.EncodeToString(existingID)
		if hex.EncodeToString(verification) == plan.Cutover.VerificationSHA256 &&
			hex.EncodeToString(lineage) == plan.Cutover.LineageSHA256 &&
			hex.EncodeToString(cursor) == plan.Cutover.CursorSHA256 &&
			hex.EncodeToString(shadow) == plan.Cutover.ShadowSHA256 {
			plan.Cutover.CutoverID = predecessor
			return true, nil
		}
		var activationCount int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_history_activation_receipts
			WHERE cutover_id=?`, existingID).Scan(&activationCount); err != nil {
			return false, err
		}
		if activationCount != 0 {
			return false, ErrEventConflict
		}
		if err := validateStoredReadyCutoverIntegrityConn(ctx, conn, existingID, lineage, cursor, shadow); err != nil {
			return false, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	digest := sha256.New()
	for _, value := range []string{HistoryCutoverContractID, plan.Cutover.BackfillID, predecessor,
		plan.Cutover.VerificationSHA256, plan.Cutover.LineageSHA256, plan.Cutover.CursorSHA256,
		plan.Cutover.ShadowSHA256} {
		writeHistoryDigestField(digest, []byte(value))
	}
	plan.Cutover.CutoverID = hex.EncodeToString(digest.Sum(nil))
	plan.Cutover.SupersedesCutoverID = predecessor
	return false, nil
}

func validateStoredReadyCutoverIntegrityConn(
	ctx context.Context, conn *sql.Conn, cutoverID, lineage, cursor, shadow []byte,
) error {
	observed, err := digestStoredHistoryCutoverRows(ctx, conn, cutoverID)
	if err != nil {
		return err
	}
	expected := hex.EncodeToString(lineage) + hex.EncodeToString(cursor) + hex.EncodeToString(shadow)
	if observed != expected {
		return ErrEventConflict
	}
	return nil
}

func historyCutoverShadowRank(dimension string) int {
	for index, value := range askUserHistoryShadowDimensions {
		if string(value) == dimension {
			return index
		}
	}
	return len(askUserHistoryShadowDimensions)
}

func validateStoredHistoryCutoverConn(
	ctx context.Context, conn *sql.Conn, plan historyCutoverPlan,
) (time.Time, bool, error) {
	cutoverID, _ := hex.DecodeString(plan.Cutover.CutoverID)
	var backfillID, supersedes, verification, lineage, cursor, shadow []byte
	var streamUID, ownerID, branchID, status, prior, target string
	var generation, through int64
	var branchCount, eventCount, cursorCount, activated int
	var createdAt time.Time
	err := conn.QueryRowContext(ctx, `SELECT backfill_id,supersedes_cutover_id,stream_uid,owner_id,source_branch_id,source_generation,
		source_through_publication_seq,verification_sha256,lineage_sha256,cursor_sha256,shadow_sha256,
		branch_count,event_count,cursor_count,prior_read_authority,target_read_authority,status,activated,created_at
		FROM transcript_history_cutover_runs WHERE cutover_id=?`, cutoverID).Scan(
		&backfillID, &supersedes, &streamUID, &ownerID, &branchID, &generation, &through, &verification, &lineage, &cursor,
		&shadow, &branchCount, &eventCount, &cursorCount, &prior, &target, &status, &activated, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if hex.EncodeToString(backfillID) != plan.Cutover.BackfillID || hex.EncodeToString(supersedes) != plan.Cutover.SupersedesCutoverID ||
		streamUID != plan.Cutover.StreamUID ||
		ownerID != plan.Cutover.OwnerID || branchID != plan.Cutover.SourceBranchID || generation != plan.Cutover.SourceGeneration ||
		through != plan.Cutover.SourceThroughPublicationSequence || hex.EncodeToString(verification) != plan.Cutover.VerificationSHA256 ||
		hex.EncodeToString(lineage) != plan.Cutover.LineageSHA256 || hex.EncodeToString(cursor) != plan.Cutover.CursorSHA256 ||
		hex.EncodeToString(shadow) != plan.Cutover.ShadowSHA256 || branchCount != len(plan.Branches) || eventCount != len(plan.Events) ||
		cursorCount != len(plan.Cursors) || prior != "legacy_mixed_v1" || target != "transcript_payload_v1" ||
		status != "ready" || activated != 0 {
		return time.Time{}, false, ErrEventConflict
	}
	observed, err := digestStoredHistoryCutoverRows(ctx, conn, cutoverID)
	expected := plan.Cutover.LineageSHA256 + plan.Cutover.CursorSHA256 + plan.Cutover.ShadowSHA256
	if err != nil || observed != expected {
		if err != nil {
			return time.Time{}, false, err
		}
		return time.Time{}, false, fmt.Errorf("stored cutover digest mismatch: %.16s:%.16s:%.16s/%.16s:%.16s:%.16s: %w",
			observed, observed[64:], observed[128:], expected, expected[64:], expected[128:], ErrEventConflict)
	}
	return createdAt, true, nil
}

func digestStoredHistoryCutoverRows(ctx context.Context, conn *sql.Conn, cutoverID []byte) (string, error) {
	lineage := sha256.New()
	rows, err := conn.QueryContext(ctx, `SELECT source_branch_id,target_branch_id,COALESCE(parent_source_branch_id,''),
		COALESCE(parent_target_branch_id,''),kind,source_fork_event_id,COALESCE(target_fork_event_key,''),
		fork_point,client_mutation_id,request_sha256,source_message_id,source_through_ordinal,
		source_through_publication_seq,source_membership_sha256,target_membership_sha256,created_at,updated_at
		FROM transcript_history_cutover_branches WHERE cutover_id=? ORDER BY source_branch_id`, cutoverID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var source, target, parentSource, parentTarget, kind string
		var targetForkKey, mutationID, sourceMessageID string
		var forkEventID sql.NullInt64
		var forkPoint int64
		var throughOrdinal, throughPublication int64
		var requestDigest, sourceDigest, targetDigest []byte
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&source, &target, &parentSource, &parentTarget, &kind, &forkEventID, &targetForkKey,
			&forkPoint, &mutationID, &requestDigest, &sourceMessageID, &throughOrdinal, &throughPublication,
			&sourceDigest, &targetDigest, &createdAt, &updatedAt); err != nil {
			_ = rows.Close()
			return "", err
		}
		membershipRows, err := conn.QueryContext(ctx, `SELECT target_ordinal,target_event_key,source_ordinal
			FROM transcript_history_cutover_branch_events WHERE cutover_id=? AND target_branch_id=? ORDER BY target_ordinal`, cutoverID, target)
		if err != nil {
			_ = rows.Close()
			return "", err
		}
		memberships := []historyCutoverMembership{}
		for membershipRows.Next() {
			var membership historyCutoverMembership
			var sourceOrdinal sql.NullInt64
			if err := membershipRows.Scan(&membership.TargetOrdinal, &membership.EventKey, &sourceOrdinal); err != nil {
				_ = membershipRows.Close()
				_ = rows.Close()
				return "", err
			}
			if sourceOrdinal.Valid {
				value := sourceOrdinal.Int64
				membership.SourceOrdinal = &value
			}
			memberships = append(memberships, membership)
		}
		if err := membershipRows.Close(); err != nil {
			_ = rows.Close()
			return "", err
		}
		if digestTargetMembership(memberships) != hex.EncodeToString(targetDigest) {
			_ = rows.Close()
			return "", ErrEventConflict
		}
		forkEvent := ""
		if forkEventID.Valid {
			forkEvent = fmt.Sprint(forkEventID.Int64)
		}
		for _, value := range []string{source, target, parentSource, parentTarget, kind, forkEvent, targetForkKey,
			fmt.Sprint(forkPoint), mutationID, hex.EncodeToString(requestDigest), sourceMessageID,
			fmt.Sprint(throughOrdinal), fmt.Sprint(throughPublication), hex.EncodeToString(sourceDigest),
			hex.EncodeToString(targetDigest), createdAt.UTC().Format(time.RFC3339Nano), updatedAt.UTC().Format(time.RFC3339Nano)} {
			writeHistoryDigestField(lineage, []byte(value))
		}
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	eventDigest := &bytes.Buffer{}
	eventRows, err := conn.QueryContext(ctx, `SELECT target_event_key,fact_kind,source_event_id,candidate_id,
		client_message_id,event_type,runner_attempt,target_publication_seq,payload_json,payload_sha256,created_at
		FROM transcript_history_cutover_events WHERE cutover_id=? ORDER BY target_publication_seq`, cutoverID)
	if err != nil {
		return "", err
	}
	for eventRows.Next() {
		var key, kind, clientID, eventType string
		var sourceEventID, runnerAttempt sql.NullInt64
		var candidateID, payload, payloadDigest []byte
		var sequence int64
		var createdAt time.Time
		if err := eventRows.Scan(&key, &kind, &sourceEventID, &candidateID, &clientID, &eventType, &runnerAttempt,
			&sequence, &payload, &payloadDigest, &createdAt); err != nil {
			_ = eventRows.Close()
			return "", err
		}
		observedPayloadDigest := sha256.Sum256(payload)
		if !equalBytes(observedPayloadDigest[:], payloadDigest) {
			_ = eventRows.Close()
			return "", ErrEventConflict
		}
		sourceEvent, attempt := "", ""
		if sourceEventID.Valid {
			sourceEvent = fmt.Sprint(sourceEventID.Int64)
		}
		if runnerAttempt.Valid {
			attempt = fmt.Sprint(runnerAttempt.Int64)
		}
		for _, value := range []string{key, kind, sourceEvent, hex.EncodeToString(candidateID), clientID, eventType,
			attempt, fmt.Sprint(sequence), hex.EncodeToString(payloadDigest), createdAt.UTC().Format(time.RFC3339Nano)} {
			writeHistoryDigestBufferField(eventDigest, []byte(value))
		}
	}
	if err := eventRows.Close(); err != nil {
		return "", err
	}
	receiptRows, err := conn.QueryContext(ctx, `SELECT source_attempt,target_event_key,status,finished_at
		FROM transcript_history_cutover_receipts WHERE cutover_id=? ORDER BY source_attempt`, cutoverID)
	if err != nil {
		return "", err
	}
	for receiptRows.Next() {
		var attempt int64
		var key, status string
		var finishedAt time.Time
		if err := receiptRows.Scan(&attempt, &key, &status, &finishedAt); err != nil {
			_ = receiptRows.Close()
			return "", err
		}
		for _, value := range []string{fmt.Sprint(attempt), key, status, finishedAt.UTC().Format(time.RFC3339Nano)} {
			writeHistoryDigestField(lineage, []byte(value))
		}
	}
	if err := receiptRows.Close(); err != nil {
		return "", err
	}
	artifactRows, err := conn.QueryContext(ctx, `SELECT target_event_key,ordinal,artifact_id,version_id,relation,availability,created_at
		FROM transcript_history_cutover_artifact_refs WHERE cutover_id=? ORDER BY target_event_key,ordinal`, cutoverID)
	if err != nil {
		return "", err
	}
	for artifactRows.Next() {
		var key, artifactID, versionID, relation, availability string
		var ordinal int
		var createdAt time.Time
		if err := artifactRows.Scan(&key, &ordinal, &artifactID, &versionID, &relation, &availability, &createdAt); err != nil {
			_ = artifactRows.Close()
			return "", err
		}
		for _, value := range []string{key, fmt.Sprint(ordinal), artifactID, versionID, relation, availability,
			createdAt.UTC().Format(time.RFC3339Nano)} {
			writeHistoryDigestField(lineage, []byte(value))
		}
	}
	if err := artifactRows.Close(); err != nil {
		return "", err
	}
	if _, err := lineage.Write(eventDigest.Bytes()); err != nil {
		return "", err
	}
	cursorDigest := sha256.New()
	cursorRows, err := conn.QueryContext(ctx, `SELECT source_branch_id,target_branch_id,source_generation,
		source_through_publication_seq,source_message_index,source_ordinal,stable_message_id,target_publication_seq,target_message_index
		FROM transcript_history_cutover_cursor_map WHERE cutover_id=? ORDER BY source_branch_id,source_message_index`, cutoverID)
	if err != nil {
		return "", err
	}
	for cursorRows.Next() {
		var source, target, stable string
		var generation, through, index, ordinal, publication, targetIndex int64
		if err := cursorRows.Scan(&source, &target, &generation, &through, &index, &ordinal, &stable, &publication, &targetIndex); err != nil {
			_ = cursorRows.Close()
			return "", err
		}
		for _, value := range []string{source, target, fmt.Sprint(generation), fmt.Sprint(through), fmt.Sprint(index), fmt.Sprint(ordinal), stable, fmt.Sprint(publication), fmt.Sprint(targetIndex)} {
			writeHistoryDigestField(cursorDigest, []byte(value))
		}
	}
	if err := cursorRows.Close(); err != nil {
		return "", err
	}
	shadowDigest := sha256.New()
	shadowRows, err := conn.QueryContext(ctx, `SELECT source_branch_id,dimension,legacy_sha256,target_sha256
		FROM transcript_history_cutover_shadow_comparisons WHERE cutover_id=? ORDER BY source_branch_id,CASE dimension
		WHEN 'stable_ids' THEN 1 WHEN 'branch_order' THEN 2 WHEN 'ask_user_states' THEN 3
		WHEN 'terminal_facts' THEN 4 WHEN 'artifact_refs' THEN 5 ELSE 6 END`, cutoverID)
	if err != nil {
		return "", err
	}
	for shadowRows.Next() {
		var branch, dimension string
		var legacy, target []byte
		if err := shadowRows.Scan(&branch, &dimension, &legacy, &target); err != nil {
			_ = shadowRows.Close()
			return "", err
		}
		if !equalBytes(legacy, target) {
			_ = shadowRows.Close()
			return "", ErrEventConflict
		}
		writeHistoryDigestField(shadowDigest, []byte(branch))
		writeHistoryDigestField(shadowDigest, []byte(dimension))
		writeHistoryDigestField(shadowDigest, []byte(hex.EncodeToString(legacy)))
	}
	if err := shadowRows.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(lineage.Sum(nil)) + hex.EncodeToString(cursorDigest.Sum(nil)) + hex.EncodeToString(shadowDigest.Sum(nil)), nil
}

func insertHistoryCutoverConn(ctx context.Context, conn *sql.Conn, plan historyCutoverPlan) error {
	cutoverID, _ := hex.DecodeString(plan.Cutover.CutoverID)
	backfillID, _ := hex.DecodeString(plan.Cutover.BackfillID)
	verification, _ := hex.DecodeString(plan.Cutover.VerificationSHA256)
	lineage, _ := hex.DecodeString(plan.Cutover.LineageSHA256)
	cursor, _ := hex.DecodeString(plan.Cutover.CursorSHA256)
	shadow, _ := hex.DecodeString(plan.Cutover.ShadowSHA256)
	var supersedes any
	if plan.Cutover.SupersedesCutoverID != "" {
		decoded, err := hex.DecodeString(plan.Cutover.SupersedesCutoverID)
		if err != nil || len(decoded) != sha256.Size {
			return ErrEventConflict
		}
		supersedes = decoded
		result, err := conn.ExecContext(ctx, `UPDATE transcript_history_cutover_runs SET status='superseded',updated_at=?
			WHERE cutover_id=? AND status='ready' AND activated=0`, plan.Cutover.CreatedAt, decoded)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			if err != nil {
				return err
			}
			return ErrEventConflict
		}
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_cutover_runs(
		cutover_id,backfill_id,supersedes_cutover_id,stream_uid,owner_id,source_branch_id,source_generation,
		source_through_publication_seq,contract_version,verification_sha256,lineage_sha256,cursor_sha256,shadow_sha256,
		branch_count,event_count,cursor_count,prior_read_authority,target_read_authority,status,activated,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,1,?,?,?,?,?,?,?,'legacy_mixed_v1','transcript_payload_v1','ready',0,?,?)`,
		cutoverID, backfillID, supersedes, plan.Cutover.StreamUID, plan.Cutover.OwnerID, plan.Cutover.SourceBranchID,
		plan.Cutover.SourceGeneration, plan.Cutover.SourceThroughPublicationSequence, verification, lineage, cursor, shadow,
		len(plan.Branches), len(plan.Events), len(plan.Cursors), plan.Cutover.CreatedAt, plan.Cutover.CreatedAt); err != nil {
		return err
	}
	for _, event := range plan.Events {
		var sourceEventID any
		if event.SourceEventID != nil {
			sourceEventID = *event.SourceEventID
		}
		var candidateID any
		if event.CandidateID != "" {
			decoded, _ := hex.DecodeString(event.CandidateID)
			candidateID = decoded
		}
		var attempt any
		if event.RunnerAttempt != nil {
			attempt = *event.RunnerAttempt
		}
		payloadDigest, _ := hex.DecodeString(event.PayloadSHA256)
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_cutover_events(
			cutover_id,target_event_key,source_event_id,candidate_id,fact_kind,target_publication_seq,
			client_message_id,event_type,runner_attempt,payload_json,payload_sha256,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, cutoverID, event.Key, sourceEventID, candidateID, event.FactKind,
			event.TargetPublicationSequence, event.ClientMessageID, event.EventType, attempt, event.PayloadJSON,
			payloadDigest, event.CreatedAt); err != nil {
			return err
		}
	}
	for _, branch := range plan.Branches {
		sourceDigest, _ := hex.DecodeString(branch.SourceMembershipSHA256)
		targetDigest, _ := hex.DecodeString(branch.TargetMembershipSHA256)
		requestDigest, _ := hex.DecodeString(branch.RequestSHA256)
		var parentSource, parentTarget any
		if branch.ParentSourceID != "" {
			parentSource, parentTarget = branch.ParentSourceID, branch.ParentTargetID
		}
		var forkEventID, targetForkEventKey any
		if branch.SourceForkEventID != nil {
			forkEventID, targetForkEventKey = *branch.SourceForkEventID, branch.TargetForkEventKey
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_cutover_branches(
			cutover_id,source_branch_id,target_branch_id,parent_source_branch_id,parent_target_branch_id,kind,
			source_fork_event_id,target_fork_event_key,fork_point,client_mutation_id,request_sha256,source_message_id,
			source_through_ordinal,source_through_publication_seq,source_membership_sha256,target_membership_sha256,
			created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, cutoverID, branch.SourceID, branch.TargetID, parentSource, parentTarget,
			branch.Kind, forkEventID, targetForkEventKey, branch.ForkPoint, branch.ClientMutationID, requestDigest,
			branch.SourceMessageID, branch.SourceThroughOrdinal, branch.SourceThroughPublicationSequence,
			sourceDigest, targetDigest, branch.CreatedAt, branch.UpdatedAt); err != nil {
			return err
		}
	}
	for _, receipt := range plan.Receipts {
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_cutover_receipts(
			cutover_id,source_attempt,target_event_key,status,finished_at) VALUES(?,?,?,?,?)`,
			cutoverID, receipt.SourceAttempt, receipt.TargetEventKey, receipt.Status, receipt.FinishedAt); err != nil {
			return err
		}
	}
	for _, ref := range plan.Artifacts {
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_cutover_artifact_refs(
			cutover_id,target_event_key,ordinal,artifact_id,version_id,relation,availability,created_at)
			VALUES(?,?,?,?,?,?,?,?)`, cutoverID, ref.TargetEventKey, ref.Ordinal, ref.ArtifactID, ref.VersionID,
			ref.Relation, ref.Availability, ref.CreatedAt); err != nil {
			return err
		}
	}
	for _, branch := range plan.Branches {
		for _, membership := range branch.Memberships {
			var sourceOrdinal any
			if membership.SourceOrdinal != nil {
				sourceOrdinal = *membership.SourceOrdinal
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_cutover_branch_events(
				cutover_id,target_branch_id,target_ordinal,target_event_key,source_ordinal) VALUES(?,?,?,?,?)`,
				cutoverID, branch.TargetID, membership.TargetOrdinal, membership.EventKey, sourceOrdinal); err != nil {
				return err
			}
		}
	}
	for _, item := range plan.Cursors {
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_cutover_cursor_map(
			cutover_id,source_branch_id,target_branch_id,source_generation,source_through_publication_seq,
			source_message_index,source_ordinal,stable_message_id,target_publication_seq,target_message_index)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, cutoverID, item.SourceBranchID, item.TargetBranchID, item.SourceGeneration,
			item.SourceThroughPublicationSequence, item.SourceMessageIndex, item.SourceOrdinal, item.StableMessageID,
			item.TargetPublicationSequence, item.TargetMessageIndex); err != nil {
			return err
		}
	}
	for _, item := range plan.Shadows {
		digest, _ := hex.DecodeString(item.Digest)
		if _, err := conn.ExecContext(ctx, `INSERT INTO transcript_history_cutover_shadow_comparisons(
			cutover_id,source_branch_id,dimension,verdict,legacy_sha256,target_sha256,compared_at)
			VALUES(?,?,?,'match',?,?,?)`, cutoverID, item.SourceBranchID, item.Dimension, digest, digest,
			plan.Cutover.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}
