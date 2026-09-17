package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// TranscriptWebProjectorVersion advances whenever reducer semantics change so
// every existing derived view is rebuilt from its immutable Transcript source.
// Version 9 normalizes legacy pre-execution rejection checkpoints whose
// completed status contradicted their failed phase. Existing source events
// remain immutable for audit while every older materialized view is rebuilt
// without a frontend compatibility path.
const TranscriptWebProjectorVersion = 11

var ErrTranscriptWebProjectionStale = errors.New("transcript Web projection state is stale")

type WebReadModelRepository struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

func NewWebReadModelRepository(writeDB, readDB *sql.DB) *WebReadModelRepository {
	return &WebReadModelRepository{writeDB: writeDB, readDB: readDB}
}

type TranscriptWebProjectionState struct {
	StreamUID                     string
	BranchID                      string
	BranchGeneration              int64
	ProjectorVersion              int
	ProjectionRevision            int64
	ThroughPublicationSequence    int64
	SourceRevision                int64
	MessageCount                  int
	VisibleMessageCount           int
	MessageArtifactReferenceCount int
	ProjectorStateJSON            []byte
	ProjectorStateSHA256          string
	SourceChainSHA256             string
	Status                        string
	LastErrorCode                 string
	UpdatedAt                     time.Time
}

type TranscriptWebMessageRecord struct {
	Ordinal                  int
	MessageID                string
	ClientMessageID          string
	Visible                  bool
	VisibleIndex             *int
	MessageJSON              []byte
	MessageSHA256            string
	FirstPublicationSequence int64
	LastPublicationSequence  int64
	UpdatedAt                time.Time
	ArtifactReferences       []TranscriptWebMessageArtifactReference
}

type TranscriptWebMessageArtifactReference struct {
	MessageOrdinal         int
	Ordinal                int
	SourceEventID          int64
	RunnerAttempt          *int64
	SourceReferenceOrdinal int
	ArtifactID             string
	VersionID              string
	Relation               ArtifactRelation
	Filename               string
	ContentType            string
	SizeBytes              int64
	Checksum               string
	Availability           ArtifactAvailability
}

type ApplyTranscriptWebProjectionInput struct {
	OwnerID                    string
	State                      TranscriptWebProjectionState
	ExpectedBranchGeneration   int64
	ExpectedThroughPublication int64
	ExpectedProjectionRevision int64
	ExpectedSourceRevision     int64
	ExpectedSourceChainSHA256  string
	ReplaceAll                 bool
	// TailReplaceFromOrdinal atomically replaces the existing visible tail
	// beginning at this ordinal. It is intentionally narrower than ReplaceAll:
	// the current contract permits replacing only the final stored message,
	// which is sufficient for an open assistant segment reset without allowing
	// an incremental projector to renumber an arbitrary history prefix.
	TailReplaceFromOrdinal int
	Messages               []TranscriptWebMessageRecord
	ArtifactReferences     []TranscriptWebMessageArtifactReference
}

type TranscriptWebMessagePageInput struct {
	OwnerID                    string
	StreamUID                  string
	BranchID                   string
	BranchGeneration           int64
	ThroughPublicationSequence int64
	SourceRevision             int64
	From                       *int
	Limit                      int
	AllowQuarantinedSnapshot   bool
}

type TranscriptWebMessagePage struct {
	State    TranscriptWebProjectionState
	Messages []TranscriptWebMessageRecord
	From     int
	Total    int
}

type TranscriptWebMessageIdentityInput struct {
	OwnerID                    string
	StreamUID                  string
	BranchID                   string
	BranchGeneration           int64
	ThroughPublicationSequence int64
	SourceRevision             int64
	Identity                   string
	AllowQuarantinedSnapshot   bool
}

func transcriptWebProjectionStateServable(
	state TranscriptWebProjectionState,
	branchGeneration, throughPublicationSequence, sourceRevision int64,
	canonicalGeneration, canonicalThrough, canonicalSourceRevision int64,
	allowQuarantinedSnapshot bool,
) bool {
	if state.BranchGeneration != branchGeneration ||
		state.ThroughPublicationSequence != throughPublicationSequence ||
		state.SourceRevision != sourceRevision {
		return false
	}
	if state.Status == "ready" {
		return canonicalGeneration == state.BranchGeneration &&
			canonicalThrough == state.ThroughPublicationSequence &&
			canonicalSourceRevision == state.SourceRevision
	}
	return allowQuarantinedSnapshot && state.Status == "quarantined" &&
		state.ProjectorVersion == TranscriptWebProjectorVersion &&
		state.VisibleMessageCount > 0 &&
		canonicalGeneration == state.BranchGeneration &&
		state.ThroughPublicationSequence <= canonicalThrough &&
		state.SourceRevision <= canonicalSourceRevision
}

// GetTranscriptWebMessageRange follows the reference message-window
// contract: omit From for the latest tail, or supply a dense visible index for
// an exact [from, from+limit) range. Stable message ids are located separately.
func (r *WebReadModelRepository) GetTranscriptWebMessageRange(
	ctx context.Context, input TranscriptWebMessagePageInput,
) (TranscriptWebMessagePage, bool, error) {
	if r == nil || r.readDB == nil {
		return TranscriptWebMessagePage{}, false, errors.New("Transcript Web read model is closed")
	}
	if strings.TrimSpace(input.OwnerID) != input.OwnerID || strings.TrimSpace(input.StreamUID) != input.StreamUID ||
		strings.TrimSpace(input.BranchID) != input.BranchID || input.OwnerID == "" || input.StreamUID == "" ||
		input.BranchID == "" || input.BranchGeneration <= 0 ||
		input.ThroughPublicationSequence < 0 || input.SourceRevision < 0 ||
		input.Limit <= 0 ||
		(input.From != nil && *input.From < 0) {
		return TranscriptWebMessagePage{}, false, errors.New("invalid Transcript Web message page request")
	}
	tx, err := r.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TranscriptWebMessagePage{}, false, fmt.Errorf("begin Transcript Web page read: %w", err)
	}
	defer tx.Rollback()
	canonicalGeneration, canonicalThrough, canonicalSourceRevision, err := transcriptWebSourceFence(
		ctx, tx, input.OwnerID, input.StreamUID, input.BranchID,
	)
	if err != nil {
		return TranscriptWebMessagePage{}, false, err
	}
	state, found, err := readTranscriptWebProjectionState(ctx, tx, input.StreamUID, input.BranchID)
	if err != nil || !found {
		return TranscriptWebMessagePage{}, found, err
	}
	if !transcriptWebProjectionStateServable(
		state,
		input.BranchGeneration, input.ThroughPublicationSequence, input.SourceRevision,
		canonicalGeneration, canonicalThrough, canonicalSourceRevision,
		input.AllowQuarantinedSnapshot,
	) {
		return TranscriptWebMessagePage{}, true, ErrTranscriptWebProjectionStale
	}
	start := max(0, state.VisibleMessageCount-input.Limit)
	if input.From != nil {
		if *input.From > state.VisibleMessageCount {
			return TranscriptWebMessagePage{}, true, errors.New("Transcript Web page range is out of bounds")
		}
		start = *input.From
	}
	end := state.VisibleMessageCount
	if input.Limit < state.VisibleMessageCount-start {
		end = start + input.Limit
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT ordinal,message_id,client_message_id,visible,visible_index,message_json,message_sha256,
			first_publication_seq,last_publication_seq,updated_at
		FROM transcript_web_messages
		WHERE stream_uid=? AND branch_id=? AND visible=1 AND visible_index>=? AND visible_index<?
		ORDER BY visible_index`, input.StreamUID, input.BranchID, start, end)
	if err != nil {
		return TranscriptWebMessagePage{}, true, fmt.Errorf("query Transcript Web message page: %w", err)
	}
	defer rows.Close()
	messages := make([]TranscriptWebMessageRecord, 0, end-start)
	for rows.Next() {
		record, err := scanTranscriptWebMessageRecord(rows)
		if err != nil {
			return TranscriptWebMessagePage{}, true, err
		}
		expected := start + len(messages)
		if record.VisibleIndex == nil || *record.VisibleIndex != expected {
			return TranscriptWebMessagePage{}, true, errors.New("Transcript Web message page index gap")
		}
		messages = append(messages, record)
	}
	if err := rows.Err(); err != nil {
		return TranscriptWebMessagePage{}, true, fmt.Errorf("iterate Transcript Web message page: %w", err)
	}
	if len(messages) != end-start {
		return TranscriptWebMessagePage{}, true, errors.New("Transcript Web message page is incomplete")
	}
	references, err := loadTranscriptWebArtifactReferencesForVisibleRange(
		ctx, tx, input.OwnerID, input.StreamUID, input.BranchID, start, end,
	)
	if err != nil {
		return TranscriptWebMessagePage{}, true, err
	}
	for index := range messages {
		messages[index].ArtifactReferences = references[messages[index].Ordinal]
		if messages[index].ArtifactReferences == nil {
			messages[index].ArtifactReferences = []TranscriptWebMessageArtifactReference{}
		}
	}
	if err := tx.Commit(); err != nil {
		return TranscriptWebMessagePage{}, true, fmt.Errorf("commit Transcript Web page read: %w", err)
	}
	return TranscriptWebMessagePage{State: state, Messages: messages, From: start, Total: state.VisibleMessageCount}, true, nil
}

func (r *WebReadModelRepository) LocateTranscriptWebMessage(
	ctx context.Context, input TranscriptWebMessageIdentityInput,
) (int, bool, error) {
	state, tx, found, err := r.beginReadyTranscriptWebRead(ctx, input)
	if err != nil || !found {
		return 0, found, err
	}
	defer tx.Rollback()
	var visibleIndex int
	if err := tx.QueryRowContext(ctx, `
		SELECT message.visible_index
		FROM transcript_web_message_identities identity
		JOIN transcript_web_messages message
			ON message.stream_uid=identity.stream_uid AND message.branch_id=identity.branch_id
			AND message.ordinal=identity.message_ordinal
		WHERE identity.stream_uid=? AND identity.branch_id=? AND identity.identity=? AND message.visible=1`,
		input.StreamUID, input.BranchID, input.Identity,
	).Scan(&visibleIndex); errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return 0, false, fmt.Errorf("commit Transcript Web locate miss: %w", err)
		}
		return 0, false, nil
	} else if err != nil {
		return 0, false, fmt.Errorf("locate Transcript Web message: %w", err)
	}
	if visibleIndex < 0 || visibleIndex >= state.VisibleMessageCount {
		return 0, false, errors.New("Transcript Web message index is invalid")
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit Transcript Web message locate: %w", err)
	}
	return visibleIndex, true, nil
}

func (r *WebReadModelRepository) GetTranscriptWebMessageByID(
	ctx context.Context, input TranscriptWebMessageIdentityInput,
) (TranscriptWebMessageRecord, bool, error) {
	_, tx, found, err := r.beginReadyTranscriptWebRead(ctx, input)
	if err != nil || !found {
		return TranscriptWebMessageRecord{}, found, err
	}
	defer tx.Rollback()
	record, err := scanTranscriptWebMessageRecord(tx.QueryRowContext(ctx, `
		SELECT ordinal,message_id,client_message_id,visible,visible_index,message_json,message_sha256,
			first_publication_seq,last_publication_seq,updated_at
		FROM transcript_web_message_identities identity
		JOIN transcript_web_messages message
			ON message.stream_uid=identity.stream_uid AND message.branch_id=identity.branch_id
			AND message.ordinal=identity.message_ordinal
		WHERE identity.stream_uid=? AND identity.branch_id=? AND identity.identity=? AND message.visible=1`,
		input.StreamUID, input.BranchID, input.Identity,
	))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return TranscriptWebMessageRecord{}, false, fmt.Errorf("commit Transcript Web message miss: %w", err)
		}
		return TranscriptWebMessageRecord{}, false, nil
	}
	if err != nil {
		return TranscriptWebMessageRecord{}, false, err
	}
	references, err := loadTranscriptWebArtifactReferencesForVisibleRange(
		ctx, tx, input.OwnerID, input.StreamUID, input.BranchID, *record.VisibleIndex, *record.VisibleIndex+1,
	)
	if err != nil {
		return TranscriptWebMessageRecord{}, false, err
	}
	record.ArtifactReferences = references[record.Ordinal]
	if record.ArtifactReferences == nil {
		record.ArtifactReferences = []TranscriptWebMessageArtifactReference{}
	}
	if err := tx.Commit(); err != nil {
		return TranscriptWebMessageRecord{}, false, fmt.Errorf("commit Transcript Web message read: %w", err)
	}
	return record, true, nil
}

func (r *WebReadModelRepository) beginReadyTranscriptWebRead(
	ctx context.Context, input TranscriptWebMessageIdentityInput,
) (TranscriptWebProjectionState, *sql.Tx, bool, error) {
	if r == nil || r.readDB == nil {
		return TranscriptWebProjectionState{}, nil, false, errors.New("Transcript Web read model is closed")
	}
	if strings.TrimSpace(input.OwnerID) != input.OwnerID || strings.TrimSpace(input.StreamUID) != input.StreamUID ||
		strings.TrimSpace(input.BranchID) != input.BranchID || strings.TrimSpace(input.Identity) != input.Identity ||
		input.OwnerID == "" || input.StreamUID == "" || input.BranchID == "" || input.Identity == "" ||
		input.BranchGeneration <= 0 || input.ThroughPublicationSequence < 0 || input.SourceRevision < 0 {
		return TranscriptWebProjectionState{}, nil, false, errors.New("invalid Transcript Web message identity request")
	}
	tx, err := r.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TranscriptWebProjectionState{}, nil, false, fmt.Errorf("begin Transcript Web message read: %w", err)
	}
	generation, through, sourceRevision, err := transcriptWebSourceFence(
		ctx, tx, input.OwnerID, input.StreamUID, input.BranchID,
	)
	if err != nil {
		_ = tx.Rollback()
		return TranscriptWebProjectionState{}, nil, false, err
	}
	state, found, err := readTranscriptWebProjectionState(ctx, tx, input.StreamUID, input.BranchID)
	if err != nil || !found {
		_ = tx.Rollback()
		return TranscriptWebProjectionState{}, nil, found, err
	}
	if !transcriptWebProjectionStateServable(
		state,
		input.BranchGeneration, input.ThroughPublicationSequence, input.SourceRevision,
		generation, through, sourceRevision,
		input.AllowQuarantinedSnapshot,
	) {
		_ = tx.Rollback()
		return TranscriptWebProjectionState{}, nil, true, ErrTranscriptWebProjectionStale
	}
	return state, tx, true, nil
}

// GetTranscriptWebProjectionCheckpoint loads the complete derived reducer
// checkpoint for recovery only. Serving code must use the ready range, locate,
// and single-message methods, which also enforce the exact source high-water.
func (r *WebReadModelRepository) GetTranscriptWebProjectionCheckpoint(
	ctx context.Context, ownerID, streamUID, branchID string,
) (TranscriptWebProjectionState, []TranscriptWebMessageRecord, bool, error) {
	if r == nil || r.readDB == nil {
		return TranscriptWebProjectionState{}, nil, false, errors.New("Transcript Web read model is closed")
	}
	if strings.TrimSpace(ownerID) != ownerID || strings.TrimSpace(streamUID) != streamUID ||
		strings.TrimSpace(branchID) != branchID || ownerID == "" || streamUID == "" || branchID == "" {
		return TranscriptWebProjectionState{}, nil, false, errors.New("owner, stream, and branch ids are required")
	}
	tx, err := r.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TranscriptWebProjectionState{}, nil, false, fmt.Errorf("begin Transcript Web projection read: %w", err)
	}
	defer tx.Rollback()
	canonicalGeneration, _, _, err := transcriptWebSourceFence(ctx, tx, ownerID, streamUID, branchID)
	if err != nil {
		return TranscriptWebProjectionState{}, nil, false, err
	}
	state, found, err := readTranscriptWebProjectionState(ctx, tx, streamUID, branchID)
	if err != nil || !found {
		return TranscriptWebProjectionState{}, nil, found, err
	}
	if canonicalGeneration != state.BranchGeneration {
		return TranscriptWebProjectionState{}, nil, true, ErrTranscriptWebProjectionStale
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT ordinal,message_id,client_message_id,visible,visible_index,message_json,message_sha256,
			first_publication_seq,last_publication_seq,updated_at
		FROM transcript_web_messages
		WHERE stream_uid=? AND branch_id=? ORDER BY ordinal`, streamUID, branchID)
	if err != nil {
		return TranscriptWebProjectionState{}, nil, false, fmt.Errorf("query Transcript Web messages: %w", err)
	}
	defer rows.Close()
	messages := make([]TranscriptWebMessageRecord, 0, state.MessageCount)
	for rows.Next() {
		var record TranscriptWebMessageRecord
		var visible int
		var updatedAt string
		if err := rows.Scan(
			&record.Ordinal, &record.MessageID, &record.ClientMessageID, &visible, &record.VisibleIndex, &record.MessageJSON,
			&record.MessageSHA256, &record.FirstPublicationSequence, &record.LastPublicationSequence, &updatedAt,
		); err != nil {
			return TranscriptWebProjectionState{}, nil, false, fmt.Errorf("scan Transcript Web message: %w", err)
		}
		record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return TranscriptWebProjectionState{}, nil, false, errors.New("invalid Transcript Web message timestamp")
		}
		record.Visible = visible == 1
		if err := validateTranscriptWebMessageRecord(record); err != nil {
			return TranscriptWebProjectionState{}, nil, false, err
		}
		messages = append(messages, record)
	}
	if err := rows.Err(); err != nil {
		return TranscriptWebProjectionState{}, nil, false, fmt.Errorf("iterate Transcript Web messages: %w", err)
	}
	if len(messages) != state.MessageCount {
		return TranscriptWebProjectionState{}, nil, false, errors.New("Transcript Web projection message count mismatch")
	}
	visible := 0
	for index, message := range messages {
		if message.Ordinal != index+1 {
			return TranscriptWebProjectionState{}, nil, false, errors.New("Transcript Web projection ordinal gap")
		}
		if message.Visible {
			if message.VisibleIndex == nil || *message.VisibleIndex != visible {
				return TranscriptWebProjectionState{}, nil, false, errors.New("Transcript Web projection visible index gap")
			}
			visible++
		}
	}
	if visible != state.VisibleMessageCount {
		return TranscriptWebProjectionState{}, nil, false, errors.New("Transcript Web projection visible count mismatch")
	}
	references, referenceCount, err := loadTranscriptWebArtifactReferenceEdges(ctx, tx, streamUID, branchID)
	if err != nil {
		return TranscriptWebProjectionState{}, nil, false, err
	}
	if referenceCount != state.MessageArtifactReferenceCount {
		return TranscriptWebProjectionState{}, nil, false, errors.New("Transcript Web projection artifact reference count mismatch")
	}
	for index := range messages {
		messages[index].ArtifactReferences = references[messages[index].Ordinal]
		if messages[index].ArtifactReferences == nil {
			messages[index].ArtifactReferences = []TranscriptWebMessageArtifactReference{}
		}
	}
	if err := tx.Commit(); err != nil {
		return TranscriptWebProjectionState{}, nil, false, fmt.Errorf("commit Transcript Web projection read: %w", err)
	}
	return state, messages, true, nil
}

func (r *WebReadModelRepository) ApplyTranscriptWebProjection(ctx context.Context, input ApplyTranscriptWebProjectionInput) error {
	if r == nil || r.writeDB == nil {
		return errors.New("Transcript Web read model is closed")
	}
	if strings.TrimSpace(input.OwnerID) != input.OwnerID || input.OwnerID == "" {
		return errors.New("Transcript Web projection owner is required")
	}
	if err := validateTranscriptWebProjectionState(input.State); err != nil {
		return err
	}
	input.ExpectedSourceChainSHA256 = strings.TrimSpace(input.ExpectedSourceChainSHA256)
	if input.ExpectedBranchGeneration < 0 || input.ExpectedThroughPublication < 0 ||
		input.ExpectedProjectionRevision < 0 || input.ExpectedSourceRevision < 0 ||
		input.TailReplaceFromOrdinal < 0 ||
		(input.ReplaceAll && input.TailReplaceFromOrdinal != 0) ||
		(input.ExpectedProjectionRevision == 0) != (input.ExpectedSourceChainSHA256 == "") ||
		(input.ExpectedSourceChainSHA256 != "" && !validTranscriptWebSHA256(input.ExpectedSourceChainSHA256)) {
		return errors.New("expected Transcript Web projection coordinates are invalid")
	}
	seenOrdinals := make(map[int]bool, len(input.Messages))
	for _, message := range input.Messages {
		if err := validateTranscriptWebMessageRecord(message); err != nil {
			return err
		}
		if message.LastPublicationSequence > input.State.ThroughPublicationSequence {
			return errors.New("Transcript Web message exceeds the projection source fence")
		}
		if seenOrdinals[message.Ordinal] {
			return errors.New("duplicate Transcript Web message ordinal")
		}
		seenOrdinals[message.Ordinal] = true
	}
	if input.TailReplaceFromOrdinal > 0 {
		if len(input.Messages) > 1 {
			return errors.New("incremental Transcript Web tail replacement is not bounded")
		}
		if len(input.Messages) == 1 && input.Messages[0].Ordinal != input.TailReplaceFromOrdinal {
			return errors.New("incremental Transcript Web tail replacement ordinal is invalid")
		}
	}
	referenceOrdinals := make(map[int]map[int]bool, len(input.Messages))
	referencePairs := make(map[int]map[string]bool, len(input.Messages))
	for _, reference := range input.ArtifactReferences {
		if err := validateTranscriptWebMessageArtifactReference(reference); err != nil {
			return err
		}
		if !seenOrdinals[reference.MessageOrdinal] {
			return errors.New("Transcript Web artifact reference message was not supplied")
		}
		if referenceOrdinals[reference.MessageOrdinal] == nil {
			referenceOrdinals[reference.MessageOrdinal] = map[int]bool{}
			referencePairs[reference.MessageOrdinal] = map[string]bool{}
		}
		if referenceOrdinals[reference.MessageOrdinal][reference.Ordinal] {
			return errors.New("duplicate Transcript Web artifact reference ordinal")
		}
		pair := reference.ArtifactID + "\x00" + reference.VersionID
		if referencePairs[reference.MessageOrdinal][pair] {
			return errors.New("duplicate Transcript Web artifact reference pair")
		}
		referenceOrdinals[reference.MessageOrdinal][reference.Ordinal] = true
		referencePairs[reference.MessageOrdinal][pair] = true
	}
	for messageOrdinal, ordinals := range referenceOrdinals {
		for ordinal := 0; ordinal < len(ordinals); ordinal++ {
			if !ordinals[ordinal] {
				return fmt.Errorf("Transcript Web artifact reference ordinal gap for message %d", messageOrdinal)
			}
		}
	}
	if input.ReplaceAll {
		if len(input.Messages) != input.State.MessageCount {
			return errors.New("replacement Transcript Web projection is incomplete")
		}
		for ordinal := 1; ordinal <= input.State.MessageCount; ordinal++ {
			if !seenOrdinals[ordinal] {
				return errors.New("replacement Transcript Web projection has an ordinal gap")
			}
		}
		if len(input.ArtifactReferences) != input.State.MessageArtifactReferenceCount {
			return errors.New("replacement Transcript Web artifact projection is incomplete")
		}
	}
	repository := NewRepository(r.writeDB)
	return repository.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		canonicalGeneration, canonicalThrough, canonicalSourceRevision, err := transcriptWebSourceFence(
			ctx, tx, input.OwnerID, input.State.StreamUID, input.State.BranchID,
		)
		if err != nil {
			return err
		}
		if canonicalGeneration != input.State.BranchGeneration ||
			input.State.ThroughPublicationSequence > canonicalThrough ||
			input.State.SourceRevision > canonicalSourceRevision ||
			(input.State.Status == "ready" && (input.State.ThroughPublicationSequence != canonicalThrough ||
				input.State.SourceRevision != canonicalSourceRevision)) {
			return ErrTranscriptWebProjectionStale
		}
		var branchGeneration, through, revision, sourceRevision int64
		var storedMessageCount, storedVisibleCount, storedArtifactReferenceCount int
		var sourceChain, storedStatus string
		err = tx.QueryRowContext(ctx, `
			SELECT branch_generation,through_publication_seq,projection_revision,source_revision,
				message_count,visible_message_count,message_artifact_reference_count,source_chain_sha256,status
			FROM transcript_web_projection_state WHERE stream_uid=? AND branch_id=?`,
			input.State.StreamUID, input.State.BranchID,
		).Scan(
			&branchGeneration, &through, &revision, &sourceRevision,
			&storedMessageCount, &storedVisibleCount, &storedArtifactReferenceCount,
			&sourceChain, &storedStatus,
		)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if input.ExpectedBranchGeneration != 0 || input.ExpectedThroughPublication != 0 ||
				input.ExpectedProjectionRevision != 0 || input.ExpectedSourceRevision != 0 ||
				input.State.ProjectionRevision != 1 || !input.ReplaceAll {
				return ErrTranscriptWebProjectionStale
			}
		case err != nil:
			return fmt.Errorf("read Transcript Web projection fence: %w", err)
		case branchGeneration != input.ExpectedBranchGeneration || through != input.ExpectedThroughPublication ||
			revision != input.ExpectedProjectionRevision ||
			sourceRevision != input.ExpectedSourceRevision ||
			sourceChain != input.ExpectedSourceChainSHA256 ||
			input.State.ProjectionRevision != revision+1:
			return ErrTranscriptWebProjectionStale
		case input.State.BranchGeneration == branchGeneration && input.State.ThroughPublicationSequence < through:
			return ErrTranscriptWebProjectionStale
		case input.State.BranchGeneration == branchGeneration &&
			input.State.SourceRevision < sourceRevision:
			return ErrTranscriptWebProjectionStale
		case input.State.BranchGeneration != branchGeneration && !input.ReplaceAll:
			return ErrTranscriptWebProjectionStale
		case storedStatus == "quarantined" && input.State.Status != "quarantined" && !input.ReplaceAll:
			return ErrTranscriptWebProjectionStale
		case storedStatus == "ready" && input.State.Status == "building" &&
			input.State.ThroughPublicationSequence == through && canonicalThrough == through &&
			input.State.SourceRevision == sourceRevision && canonicalSourceRevision == sourceRevision:
			return ErrTranscriptWebProjectionStale
		}
		if input.TailReplaceFromOrdinal > 0 {
			if input.TailReplaceFromOrdinal != storedMessageCount || storedMessageCount <= 0 || storedVisibleCount <= 0 {
				return errors.New("incremental Transcript Web tail replacement is not the visible tail")
			}
			var tailVisible int
			var tailVisibleIndex sql.NullInt64
			var tailArtifactCount int
			if err := tx.QueryRowContext(ctx, `
				SELECT message.visible,message.visible_index,
					(SELECT COUNT(*) FROM transcript_web_message_artifact_refs reference
					 WHERE reference.stream_uid=message.stream_uid AND reference.branch_id=message.branch_id
						AND reference.message_ordinal=message.ordinal)
				FROM transcript_web_messages message
				WHERE stream_uid=? AND branch_id=? AND ordinal=?`,
				input.State.StreamUID, input.State.BranchID, input.TailReplaceFromOrdinal,
			).Scan(&tailVisible, &tailVisibleIndex, &tailArtifactCount); err != nil {
				return fmt.Errorf("read Transcript Web replacement tail: %w", err)
			}
			if tailVisible != 1 || !tailVisibleIndex.Valid || int(tailVisibleIndex.Int64) != storedVisibleCount-1 {
				return errors.New("incremental Transcript Web replacement target is not the visible tail")
			}
			replacementVisible := 0
			if len(input.Messages) == 1 && input.Messages[0].Visible {
				replacementVisible = 1
				if input.Messages[0].VisibleIndex == nil || *input.Messages[0].VisibleIndex != storedVisibleCount-1 {
					return errors.New("incremental Transcript Web replacement visible index is invalid")
				}
			}
			expectedMessageCount := storedMessageCount - 1 + len(input.Messages)
			expectedVisibleCount := storedVisibleCount - 1 + replacementVisible
			expectedArtifactCount := storedArtifactReferenceCount - tailArtifactCount + len(input.ArtifactReferences)
			if input.State.MessageCount != expectedMessageCount ||
				input.State.VisibleMessageCount != expectedVisibleCount ||
				input.State.MessageArtifactReferenceCount != expectedArtifactCount {
				return errors.New("incremental Transcript Web tail replacement count mismatch")
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_web_messages
				WHERE stream_uid=? AND branch_id=? AND ordinal=?`,
				input.State.StreamUID, input.State.BranchID, input.TailReplaceFromOrdinal); err != nil {
				return fmt.Errorf("replace Transcript Web message tail: %w", err)
			}
		} else if !input.ReplaceAll {
			type previousMessage struct {
				visible          bool
				messageID        string
				clientMessageID  string
				firstPublication int64
				artifactCount    int
			}
			previous := make(map[int]previousMessage, len(input.Messages))
			newOrdinals := make(map[int]bool, len(input.Messages))
			expectedMessageCount := storedMessageCount
			expectedVisibleCount := storedVisibleCount
			expectedArtifactCount := storedArtifactReferenceCount
			newArtifactCounts := make(map[int]int, len(input.Messages))
			for _, reference := range input.ArtifactReferences {
				newArtifactCounts[reference.MessageOrdinal]++
			}
			for _, message := range input.Messages {
				var visible int
				var prior previousMessage
				err := tx.QueryRowContext(ctx, `
					SELECT visible,message_id,client_message_id,first_publication_seq,
						(SELECT COUNT(*) FROM transcript_web_message_artifact_refs reference
						 WHERE reference.stream_uid=message.stream_uid AND reference.branch_id=message.branch_id
							AND reference.message_ordinal=message.ordinal)
					FROM transcript_web_messages message
					WHERE stream_uid=? AND branch_id=? AND ordinal=?`,
					input.State.StreamUID, input.State.BranchID, message.Ordinal,
				).Scan(&visible, &prior.messageID, &prior.clientMessageID, &prior.firstPublication, &prior.artifactCount)
				switch {
				case errors.Is(err, sql.ErrNoRows):
					newOrdinals[message.Ordinal] = true
					expectedMessageCount++
				case err != nil:
					return fmt.Errorf("read previous Transcript Web message: %w", err)
				default:
					prior.visible = visible == 1
					if prior.messageID != message.MessageID || prior.clientMessageID != message.ClientMessageID ||
						prior.firstPublication != message.FirstPublicationSequence {
						return ErrTranscriptWebProjectionStale
					}
					previous[message.Ordinal] = prior
					expectedArtifactCount -= prior.artifactCount
				}
				expectedArtifactCount += newArtifactCounts[message.Ordinal]
				if prior, exists := previous[message.Ordinal]; exists {
					if prior.visible != message.Visible {
						if message.Visible {
							expectedVisibleCount++
						} else {
							expectedVisibleCount--
						}
					}
				} else if message.Visible {
					expectedVisibleCount++
				}
			}
			for ordinal := storedMessageCount + 1; ordinal <= expectedMessageCount; ordinal++ {
				if !newOrdinals[ordinal] {
					return errors.New("incremental Transcript Web projection has an ordinal gap")
				}
			}
			if len(newOrdinals) != expectedMessageCount-storedMessageCount ||
				input.State.MessageCount != expectedMessageCount ||
				input.State.VisibleMessageCount != expectedVisibleCount ||
				input.State.MessageArtifactReferenceCount != expectedArtifactCount {
				return errors.New("incremental Transcript Web projection count mismatch")
			}
			for _, message := range input.Messages {
				if message.Visible && (message.VisibleIndex == nil || *message.VisibleIndex >= input.State.VisibleMessageCount) {
					return errors.New("incremental Transcript Web visible index is out of bounds")
				}
			}
			for ordinal := range previous {
				if _, err := tx.ExecContext(ctx, `UPDATE transcript_web_messages SET visible=0,visible_index=NULL
					WHERE stream_uid=? AND branch_id=? AND ordinal=?`,
					input.State.StreamUID, input.State.BranchID, ordinal); err != nil {
					return fmt.Errorf("prepare Transcript Web message reindex: %w", err)
				}
			}
		}
		if input.ReplaceAll {
			if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_web_messages WHERE stream_uid=? AND branch_id=?`,
				input.State.StreamUID, input.State.BranchID); err != nil {
				return fmt.Errorf("replace Transcript Web projection messages: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transcript_web_projection_state (
				stream_uid,branch_id,branch_generation,projector_version,projection_revision,through_publication_seq,
				source_revision,message_count,visible_message_count,message_artifact_reference_count,projector_state_json,
				projector_state_sha256,source_chain_sha256,status,last_error_code,updated_at
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(stream_uid,branch_id) DO UPDATE SET
				branch_generation=excluded.branch_generation,projector_version=excluded.projector_version,
				projection_revision=excluded.projection_revision,
				through_publication_seq=excluded.through_publication_seq,
				source_revision=excluded.source_revision,message_count=excluded.message_count,
				visible_message_count=excluded.visible_message_count,
				message_artifact_reference_count=excluded.message_artifact_reference_count,
				projector_state_json=excluded.projector_state_json,
				projector_state_sha256=excluded.projector_state_sha256,source_chain_sha256=excluded.source_chain_sha256,
				status=excluded.status,last_error_code=excluded.last_error_code,updated_at=excluded.updated_at`,
			input.State.StreamUID, input.State.BranchID, input.State.BranchGeneration,
			input.State.ProjectorVersion, input.State.ProjectionRevision, input.State.ThroughPublicationSequence,
			input.State.SourceRevision, input.State.MessageCount,
			input.State.VisibleMessageCount, input.State.MessageArtifactReferenceCount,
			string(input.State.ProjectorStateJSON), input.State.ProjectorStateSHA256,
			input.State.SourceChainSHA256, input.State.Status, input.State.LastErrorCode,
			input.State.UpdatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("write Transcript Web projection state: %w", err)
		}
		for _, message := range input.Messages {
			result, err := tx.ExecContext(ctx, `
				INSERT INTO transcript_web_messages (
					stream_uid,branch_id,ordinal,message_id,client_message_id,visible,visible_index,message_json,message_sha256,
					first_publication_seq,last_publication_seq,updated_at
				) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(stream_uid,branch_id,ordinal) DO UPDATE SET
					visible=excluded.visible,visible_index=excluded.visible_index,
					message_json=excluded.message_json,message_sha256=excluded.message_sha256,
					last_publication_seq=excluded.last_publication_seq,updated_at=excluded.updated_at
				WHERE transcript_web_messages.message_id=excluded.message_id
					AND transcript_web_messages.client_message_id=excluded.client_message_id
					AND transcript_web_messages.first_publication_seq=excluded.first_publication_seq
					AND excluded.last_publication_seq>=transcript_web_messages.last_publication_seq`,
				input.State.StreamUID, input.State.BranchID, message.Ordinal, message.MessageID,
				message.ClientMessageID, transcriptWebBoolToInteger(message.Visible), message.VisibleIndex,
				string(message.MessageJSON), message.MessageSHA256,
				message.FirstPublicationSequence, message.LastPublicationSequence, message.UpdatedAt.UTC().Format(time.RFC3339Nano),
			)
			if err != nil {
				return fmt.Errorf("write Transcript Web projection message: %w", err)
			}
			if affected, err := result.RowsAffected(); err != nil || affected != 1 {
				if err != nil {
					return fmt.Errorf("verify Transcript Web projection message write: %w", err)
				}
				return ErrTranscriptWebProjectionStale
			}
		}
		for _, message := range input.Messages {
			if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_web_message_identities
				WHERE stream_uid=? AND branch_id=? AND message_ordinal=?`,
				input.State.StreamUID, input.State.BranchID, message.Ordinal); err != nil {
				return fmt.Errorf("replace Transcript Web message identities: %w", err)
			}
			identities := []struct {
				value string
				kind  string
			}{{value: message.MessageID, kind: "message_id"}, {value: message.ClientMessageID, kind: "client_message_id"}}
			if message.MessageID == message.ClientMessageID {
				identities = identities[:1]
				identities[0].kind = "both"
			}
			for _, identity := range identities {
				if _, err := tx.ExecContext(ctx, `INSERT INTO transcript_web_message_identities(
					stream_uid,branch_id,identity,message_ordinal,kind
				) VALUES(?,?,?,?,?)`, input.State.StreamUID, input.State.BranchID,
					identity.value, message.Ordinal, identity.kind); err != nil {
					return fmt.Errorf("%w: write Transcript Web message identity: %v", ErrTranscriptWebProjectionStale, err)
				}
			}
		}
		for _, message := range input.Messages {
			if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_web_message_artifact_refs
				WHERE stream_uid=? AND branch_id=? AND message_ordinal=?`,
				input.State.StreamUID, input.State.BranchID, message.Ordinal); err != nil {
				return fmt.Errorf("replace Transcript Web message artifact references: %w", err)
			}
		}
		messagesByOrdinal := make(map[int]TranscriptWebMessageRecord, len(input.Messages))
		for _, message := range input.Messages {
			messagesByOrdinal[message.Ordinal] = message
		}
		for _, reference := range input.ArtifactReferences {
			message := messagesByOrdinal[reference.MessageOrdinal]
			if err := validateTranscriptWebArtifactReferenceSource(
				ctx, tx, input.State.StreamUID, input.State.BranchID, message, reference,
			); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO transcript_web_message_artifact_refs(
				stream_uid,branch_id,message_ordinal,ordinal,source_event_id,runner_attempt,source_reference_ordinal,
				artifact_id,version_id,relation,filename,content_type,size_bytes,checksum
			) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				input.State.StreamUID, input.State.BranchID, reference.MessageOrdinal, reference.Ordinal,
				reference.SourceEventID, reference.RunnerAttempt, reference.SourceReferenceOrdinal,
				reference.ArtifactID, reference.VersionID, string(reference.Relation), reference.Filename,
				reference.ContentType, reference.SizeBytes, reference.Checksum,
			); err != nil {
				return fmt.Errorf("write Transcript Web message artifact reference: %w", err)
			}
		}
		if input.ReplaceAll {
			var storedCount, minOrdinal, maxOrdinal, storedVisible, minVisible, maxVisible int
			if err := tx.QueryRowContext(ctx, `
				SELECT COUNT(*),COALESCE(MIN(ordinal),0),COALESCE(MAX(ordinal),0),COALESCE(SUM(visible),0),
					COALESCE(MIN(CASE WHEN visible=1 THEN visible_index END),-1),
					COALESCE(MAX(CASE WHEN visible=1 THEN visible_index END),-1)
				FROM transcript_web_messages
				WHERE stream_uid=? AND branch_id=?`, input.State.StreamUID, input.State.BranchID).
				Scan(&storedCount, &minOrdinal, &maxOrdinal, &storedVisible, &minVisible, &maxVisible); err != nil {
				return fmt.Errorf("verify replacement Transcript Web projection counts: %w", err)
			}
			if storedCount != input.State.MessageCount || storedVisible != input.State.VisibleMessageCount ||
				(storedCount == 0 && (minOrdinal != 0 || maxOrdinal != 0)) ||
				(storedCount > 0 && (minOrdinal != 1 || maxOrdinal != storedCount)) ||
				(storedVisible == 0 && (minVisible != -1 || maxVisible != -1)) ||
				(storedVisible > 0 && (minVisible != 0 || maxVisible != storedVisible-1)) {
				return errors.New("replacement Transcript Web projection write count mismatch")
			}
			var storedIdentities, expectedIdentities int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_web_message_identities
				WHERE stream_uid=? AND branch_id=?`, input.State.StreamUID, input.State.BranchID).
				Scan(&storedIdentities); err != nil {
				return fmt.Errorf("verify replacement Transcript Web message identity count: %w", err)
			}
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN message_id=client_message_id THEN 1 ELSE 2 END),0)
				FROM transcript_web_messages WHERE stream_uid=? AND branch_id=?`, input.State.StreamUID, input.State.BranchID).
				Scan(&expectedIdentities); err != nil {
				return fmt.Errorf("calculate replacement Transcript Web message identity count: %w", err)
			}
			if storedIdentities != expectedIdentities {
				return errors.New("replacement Transcript Web message identity count mismatch")
			}
			var storedArtifactReferences int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_web_message_artifact_refs
				WHERE stream_uid=? AND branch_id=?`, input.State.StreamUID, input.State.BranchID).
				Scan(&storedArtifactReferences); err != nil {
				return fmt.Errorf("verify replacement Transcript Web artifact reference count: %w", err)
			}
			if storedArtifactReferences != input.State.MessageArtifactReferenceCount {
				return errors.New("replacement Transcript Web artifact reference write count mismatch")
			}
		} else {
			var invalidVisible int
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
				SELECT 1 FROM transcript_web_messages
				WHERE stream_uid=? AND branch_id=? AND visible=1
					AND (visible_index<0 OR visible_index>=?)
			)`, input.State.StreamUID, input.State.BranchID, input.State.VisibleMessageCount).Scan(&invalidVisible); err != nil {
				return fmt.Errorf("verify incremental Transcript Web visible range: %w", err)
			}
			if invalidVisible != 0 {
				return errors.New("incremental Transcript Web visible index range mismatch")
			}
		}
		if input.State.Status == "ready" || input.State.Status == "quarantined" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_web_projection_dirty
				WHERE stream_uid=? AND branch_id=? AND source_revision<=?`,
				input.State.StreamUID, input.State.BranchID, input.State.SourceRevision); err != nil {
				return fmt.Errorf("clear Transcript Web projection dirty state: %w", err)
			}
		}
		return nil
	})
}

type transcriptWebProjectionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func transcriptWebSourceFence(
	ctx context.Context,
	db transcriptWebProjectionQuerier,
	ownerID, streamUID, branchID string,
) (int64, int64, int64, error) {
	var storedOwnerID, kind, sessionID string
	var epoch int64
	if err := db.QueryRowContext(ctx, `
		SELECT owner_id,kind,session_id,epoch FROM transcript_streams WHERE stream_uid=?`, streamUID,
	).Scan(&storedOwnerID, &kind, &sessionID, &epoch); errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, ErrBranchStateStale
	} else if err != nil {
		return 0, 0, 0, fmt.Errorf("read Transcript Web stream authority: %w", err)
	}
	if storedOwnerID != ownerID {
		return 0, 0, 0, ErrOwnerMismatch
	}
	if StreamKind(kind) == StreamKindFrameRef {
		var authority FrameAuthority
		if err := db.QueryRowContext(ctx, `
			SELECT owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
				read_authority,write_authority,activation_id,genesis_id,updated_at
			FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`, ownerID, sessionID,
		).Scan(
			&authority.OwnerID, &authority.SessionID, &authority.ActiveStreamUID, &authority.ActiveEpoch,
			&authority.AuthorityGeneration, &authority.ReadAuthority, &authority.WriteAuthority,
			&authority.ActivationID, &authority.GenesisID, &authority.UpdatedAt,
		); errors.Is(err, sql.ErrNoRows) {
			return 0, 0, 0, ErrBranchStateStale
		} else if err != nil {
			return 0, 0, 0, fmt.Errorf("read Transcript Web frame authority: %w", err)
		}
		if authority.ActiveStreamUID != streamUID || authority.ActiveEpoch != epoch ||
			!authority.CanonicalProjectionReadable() {
			return 0, 0, 0, ErrBranchStateStale
		}
	}
	var generation, through, sourceRevision int64
	if err := db.QueryRowContext(ctx, `
		SELECT branch_state.generation,head.through_publication_seq,head.source_revision
		FROM transcript_branches branch
		JOIN transcript_branch_state branch_state ON branch_state.stream_uid=branch.stream_uid
		JOIN transcript_branch_heads head
			ON head.stream_uid=branch.stream_uid AND head.branch_id=branch.branch_id
		WHERE branch.stream_uid=? AND branch.branch_id=?
	`, streamUID, branchID,
	).Scan(&generation, &through, &sourceRevision); errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, ErrBranchStateStale
	} else if err != nil {
		return 0, 0, 0, fmt.Errorf("read canonical Transcript Web source fence: %w", err)
	}
	return generation, through, sourceRevision, nil
}

type transcriptWebMessageScanner interface {
	Scan(...any) error
}

func scanTranscriptWebMessageRecord(scanner transcriptWebMessageScanner) (TranscriptWebMessageRecord, error) {
	var record TranscriptWebMessageRecord
	var visible int
	var updatedAt string
	if err := scanner.Scan(
		&record.Ordinal, &record.MessageID, &record.ClientMessageID, &visible, &record.VisibleIndex,
		&record.MessageJSON, &record.MessageSHA256, &record.FirstPublicationSequence,
		&record.LastPublicationSequence, &updatedAt,
	); err != nil {
		return TranscriptWebMessageRecord{}, fmt.Errorf("scan Transcript Web message: %w", err)
	}
	record.Visible = visible == 1
	parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return TranscriptWebMessageRecord{}, errors.New("invalid Transcript Web message timestamp")
	}
	record.UpdatedAt = parsed
	if err := validateTranscriptWebMessageRecord(record); err != nil {
		return TranscriptWebMessageRecord{}, err
	}
	return record, nil
}

func loadTranscriptWebArtifactReferencesForVisibleRange(
	ctx context.Context,
	tx *sql.Tx,
	ownerID, streamUID, branchID string,
	from, through int,
) (map[int][]TranscriptWebMessageArtifactReference, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT message.ordinal,reference.ordinal,reference.source_event_id,reference.runner_attempt,
			reference.source_reference_ordinal,reference.artifact_id,reference.version_id,reference.relation,
			reference.filename,reference.content_type,reference.size_bytes,reference.checksum,
			canonical.source_event_id,canonical.availability,tombstone.version_id,
			live_project.id,live_artifact.id,live_artifact.name,live_artifact.kind,live_version.id,
			CASE WHEN COALESCE(live_version.storage_path,'')=''
				THEN length(live_version.content) ELSE live_version.size_bytes END,
			live_version.content_sha256
		FROM transcript_web_messages message
		JOIN transcript_web_message_artifact_refs reference
			ON reference.stream_uid=message.stream_uid AND reference.branch_id=message.branch_id
			AND reference.message_ordinal=message.ordinal
		JOIN transcript_streams stream ON stream.stream_uid=message.stream_uid
		LEFT JOIN transcript_artifact_refs canonical
			ON canonical.stream_uid=reference.stream_uid
			AND canonical.runner_attempt=reference.runner_attempt
			AND canonical.source_event_id=reference.source_event_id
			AND canonical.ordinal=reference.source_reference_ordinal
			AND canonical.artifact_id=reference.artifact_id AND canonical.version_id=reference.version_id
			AND canonical.relation=reference.relation
		LEFT JOIN artifact_version_tombstones tombstone
			ON tombstone.artifact_id=reference.artifact_id AND tombstone.version_id=reference.version_id
			AND tombstone.owner_id=stream.owner_id AND tombstone.project_id=stream.project_id
		LEFT JOIN projects live_project
			ON live_project.id=stream.project_id AND live_project.user_id=stream.owner_id
		LEFT JOIN artifacts live_artifact
			ON live_artifact.id=reference.artifact_id AND live_artifact.project_id=live_project.id
		LEFT JOIN artifact_versions live_version
			ON live_version.id=reference.version_id AND live_version.artifact_id=live_artifact.id
		WHERE message.stream_uid=? AND message.branch_id=? AND stream.owner_id=?
			AND message.visible=1 AND message.visible_index>=? AND message.visible_index<?
		ORDER BY message.visible_index,reference.ordinal`, streamUID, branchID, ownerID, from, through)
	if err != nil {
		return nil, fmt.Errorf("query Transcript Web artifact references: %w", err)
	}
	defer rows.Close()
	result := map[int][]TranscriptWebMessageArtifactReference{}
	for rows.Next() {
		var reference TranscriptWebMessageArtifactReference
		var runnerAttempt, canonicalSourceEvent, liveSize sql.NullInt64
		var relation string
		var canonicalAvailability, tombstoneVersion, liveProjectID, liveArtifactID sql.NullString
		var liveName, liveKind, liveVersionID, liveChecksum sql.NullString
		if err := rows.Scan(
			&reference.MessageOrdinal, &reference.Ordinal, &reference.SourceEventID, &runnerAttempt,
			&reference.SourceReferenceOrdinal, &reference.ArtifactID, &reference.VersionID, &relation,
			&reference.Filename, &reference.ContentType, &reference.SizeBytes, &reference.Checksum,
			&canonicalSourceEvent, &canonicalAvailability, &tombstoneVersion,
			&liveProjectID, &liveArtifactID, &liveName, &liveKind, &liveVersionID, &liveSize, &liveChecksum,
		); err != nil {
			return nil, fmt.Errorf("scan Transcript Web artifact reference: %w", err)
		}
		reference.Relation = ArtifactRelation(relation)
		if runnerAttempt.Valid {
			attempt := runnerAttempt.Int64
			reference.RunnerAttempt = &attempt
			if !canonicalSourceEvent.Valid {
				return nil, errors.New("Transcript Web runner artifact reference is structurally unavailable")
			}
		} else if canonicalSourceEvent.Valid {
			return nil, errors.New("Transcript Web user artifact reference resolved to a runner ledger")
		}
		switch {
		case tombstoneVersion.Valid || (canonicalAvailability.Valid && canonicalAvailability.String == string(ArtifactDeleted)):
			reference.Availability = ArtifactDeleted
		case canonicalAvailability.Valid && canonicalAvailability.String == string(ArtifactMissing):
			reference.Availability = ArtifactMissing
		case !liveProjectID.Valid || !liveArtifactID.Valid || !liveVersionID.Valid:
			reference.Availability = ArtifactMissing
		default:
			reference.Availability = ArtifactAvailable
		}
		if reference.RunnerAttempt == nil && reference.Availability == ArtifactAvailable {
			if !liveSize.Valid || !liveChecksum.Valid || liveSize.Int64 != reference.SizeBytes ||
				liveChecksum.String != reference.Checksum {
				return nil, errors.New("Transcript Web user artifact snapshot no longer matches its exact version")
			}
		}
		if reference.RunnerAttempt != nil && reference.Availability == ArtifactAvailable {
			if !liveName.Valid || !liveKind.Valid || !liveSize.Valid || !liveChecksum.Valid {
				return nil, errors.New("Transcript Web runner artifact metadata is incomplete")
			}
			reference.Filename = liveName.String
			reference.ContentType = liveKind.String
			reference.SizeBytes = liveSize.Int64
			reference.Checksum = liveChecksum.String
		}
		if err := validateServedTranscriptWebMessageArtifactReference(reference); err != nil {
			return nil, err
		}
		result[reference.MessageOrdinal] = append(result[reference.MessageOrdinal], reference)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Transcript Web artifact references: %w", err)
	}
	for messageOrdinal, references := range result {
		for ordinal := range references {
			if references[ordinal].Ordinal != ordinal {
				return nil, fmt.Errorf("Transcript Web artifact reference gap for message %d", messageOrdinal)
			}
		}
	}
	return result, nil
}

func loadTranscriptWebArtifactReferenceEdges(
	ctx context.Context,
	tx *sql.Tx,
	streamUID, branchID string,
) (map[int][]TranscriptWebMessageArtifactReference, int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT message_ordinal,ordinal,source_event_id,runner_attempt,
		source_reference_ordinal,artifact_id,version_id,relation,filename,content_type,size_bytes,checksum
		FROM transcript_web_message_artifact_refs
		WHERE stream_uid=? AND branch_id=? ORDER BY message_ordinal,ordinal`, streamUID, branchID)
	if err != nil {
		return nil, 0, fmt.Errorf("query Transcript Web artifact reference edges: %w", err)
	}
	defer rows.Close()
	result := map[int][]TranscriptWebMessageArtifactReference{}
	count := 0
	for rows.Next() {
		var reference TranscriptWebMessageArtifactReference
		var runnerAttempt sql.NullInt64
		var relation string
		if err := rows.Scan(
			&reference.MessageOrdinal, &reference.Ordinal, &reference.SourceEventID, &runnerAttempt,
			&reference.SourceReferenceOrdinal, &reference.ArtifactID, &reference.VersionID, &relation,
			&reference.Filename, &reference.ContentType, &reference.SizeBytes, &reference.Checksum,
		); err != nil {
			return nil, 0, fmt.Errorf("scan Transcript Web artifact reference edge: %w", err)
		}
		if runnerAttempt.Valid {
			attempt := runnerAttempt.Int64
			reference.RunnerAttempt = &attempt
		}
		reference.Relation = ArtifactRelation(relation)
		if err := validateTranscriptWebMessageArtifactReference(reference); err != nil {
			return nil, 0, err
		}
		references := result[reference.MessageOrdinal]
		if reference.Ordinal != len(references) {
			return nil, 0, errors.New("Transcript Web artifact reference edge gap")
		}
		result[reference.MessageOrdinal] = append(references, reference)
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate Transcript Web artifact reference edges: %w", err)
	}
	return result, count, nil
}

func readTranscriptWebProjectionState(
	ctx context.Context, db transcriptWebProjectionQuerier, streamUID, branchID string,
) (TranscriptWebProjectionState, bool, error) {
	return readTranscriptWebProjectionStateVersioned(ctx, db, streamUID, branchID, false)
}

func readTranscriptWebProjectionStateForUpgrade(
	ctx context.Context, db transcriptWebProjectionQuerier, streamUID, branchID string,
) (TranscriptWebProjectionState, bool, error) {
	return readTranscriptWebProjectionStateVersioned(ctx, db, streamUID, branchID, true)
}

func readTranscriptWebProjectionStateVersioned(
	ctx context.Context,
	db transcriptWebProjectionQuerier,
	streamUID, branchID string,
	allowOlderProjector bool,
) (TranscriptWebProjectionState, bool, error) {
	var state TranscriptWebProjectionState
	var updatedAt string
	err := db.QueryRowContext(ctx, `
		SELECT stream_uid,branch_id,branch_generation,projector_version,projection_revision,through_publication_seq,
			source_revision,message_count,visible_message_count,message_artifact_reference_count,
			projector_state_json,projector_state_sha256,
			source_chain_sha256,status,last_error_code,updated_at
		FROM transcript_web_projection_state WHERE stream_uid=? AND branch_id=?`, streamUID, branchID).
		Scan(
			&state.StreamUID, &state.BranchID, &state.BranchGeneration, &state.ProjectorVersion,
			&state.ProjectionRevision, &state.ThroughPublicationSequence, &state.SourceRevision,
			&state.MessageCount, &state.VisibleMessageCount, &state.MessageArtifactReferenceCount,
			&state.ProjectorStateJSON, &state.ProjectorStateSHA256, &state.SourceChainSHA256,
			&state.Status, &state.LastErrorCode, &updatedAt,
		)
	if errors.Is(err, sql.ErrNoRows) {
		return TranscriptWebProjectionState{}, false, nil
	}
	if err != nil {
		return TranscriptWebProjectionState{}, false, fmt.Errorf("read Transcript Web projection state: %w", err)
	}
	state.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return TranscriptWebProjectionState{}, false, errors.New("invalid Transcript Web projection timestamp")
	}
	if err := validateTranscriptWebProjectionStateVersioned(state, allowOlderProjector); err != nil {
		return TranscriptWebProjectionState{}, false, err
	}
	return state, true, nil
}

func validateTranscriptWebProjectionState(state TranscriptWebProjectionState) error {
	return validateTranscriptWebProjectionStateVersioned(state, false)
}

func validateTranscriptWebProjectionStateVersioned(
	state TranscriptWebProjectionState,
	allowOlderProjector bool,
) error {
	projectorVersionValid := state.ProjectorVersion == TranscriptWebProjectorVersion
	if allowOlderProjector {
		projectorVersionValid = state.ProjectorVersion >= 1 && state.ProjectorVersion <= TranscriptWebProjectorVersion
	}
	if strings.TrimSpace(state.StreamUID) != state.StreamUID || strings.TrimSpace(state.BranchID) != state.BranchID ||
		state.StreamUID == "" || state.BranchID == "" || state.BranchGeneration <= 0 ||
		!projectorVersionValid || state.ProjectionRevision <= 0 ||
		state.ThroughPublicationSequence < 0 || state.SourceRevision < 0 ||
		state.MessageCount < 0 || state.VisibleMessageCount < 0 || state.VisibleMessageCount > state.MessageCount ||
		state.MessageArtifactReferenceCount < 0 ||
		(state.ThroughPublicationSequence == 0 && state.MessageCount != 0) {
		return errors.New("invalid Transcript Web projection state")
	}
	if !json.Valid(state.ProjectorStateJSON) || len(state.ProjectorStateJSON) == 0 {
		return errors.New("invalid Transcript Web projector state JSON")
	}
	if !matchesTranscriptWebSHA256(state.ProjectorStateJSON, state.ProjectorStateSHA256) ||
		!validTranscriptWebSHA256(state.SourceChainSHA256) {
		return errors.New("invalid Transcript Web projection digest")
	}
	state.Status, state.LastErrorCode = strings.TrimSpace(state.Status), strings.TrimSpace(state.LastErrorCode)
	if (state.Status != "building" && state.Status != "ready" && state.Status != "quarantined") ||
		(state.Status == "quarantined") != (state.LastErrorCode != "") || state.UpdatedAt.IsZero() {
		return errors.New("invalid Transcript Web projection status")
	}
	return nil
}

func validateTranscriptWebMessageRecord(record TranscriptWebMessageRecord) error {
	if strings.TrimSpace(record.MessageID) != record.MessageID ||
		strings.TrimSpace(record.ClientMessageID) != record.ClientMessageID ||
		record.Ordinal <= 0 || record.MessageID == "" || record.ClientMessageID == "" ||
		record.FirstPublicationSequence <= 0 || record.LastPublicationSequence < record.FirstPublicationSequence ||
		record.UpdatedAt.IsZero() || len(record.MessageJSON) == 0 ||
		!json.Valid(record.MessageJSON) || !matchesTranscriptWebSHA256(record.MessageJSON, record.MessageSHA256) {
		return errors.New("invalid Transcript Web message record")
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(record.MessageJSON, &envelope) != nil {
		return errors.New("invalid Transcript Web message envelope")
	}
	if _, embedsArtifactReferences := envelope["artifact_refs"]; embedsArtifactReferences {
		return errors.New("Transcript Web message embeds mutable artifact references")
	}
	var envelopeID, envelopeClientMessageID string
	if json.Unmarshal(envelope["id"], &envelopeID) != nil ||
		json.Unmarshal(envelope["msg_id"], &envelopeClientMessageID) != nil ||
		envelopeID != record.MessageID || envelopeClientMessageID != record.ClientMessageID {
		return errors.New("Transcript Web message identity envelope mismatch")
	}
	if record.Visible {
		if record.VisibleIndex == nil || *record.VisibleIndex < 0 {
			return errors.New("invalid visible Transcript Web message index")
		}
	} else if record.VisibleIndex != nil {
		return errors.New("hidden Transcript Web message has a visible index")
	}
	return nil
}

func validateTranscriptWebMessageArtifactReference(reference TranscriptWebMessageArtifactReference) error {
	if reference.MessageOrdinal <= 0 || reference.Ordinal < 0 || reference.SourceEventID <= 0 ||
		reference.SourceReferenceOrdinal < 0 || strings.TrimSpace(reference.ArtifactID) != reference.ArtifactID ||
		strings.TrimSpace(reference.VersionID) != reference.VersionID || reference.ArtifactID == "" || reference.VersionID == "" ||
		reference.Availability != "" || reference.SizeBytes < 0 ||
		(reference.Relation != ArtifactRelationProduced && reference.Relation != ArtifactRelationConsumed &&
			reference.Relation != ArtifactRelationCited && reference.Relation != ArtifactRelationAttached) {
		return errors.New("invalid Transcript Web message artifact reference")
	}
	if reference.RunnerAttempt != nil {
		if *reference.RunnerAttempt <= 0 || reference.Filename != "" || reference.ContentType != "" ||
			reference.SizeBytes != 0 || reference.Checksum != "" {
			return errors.New("invalid runner Transcript Web artifact reference")
		}
		return nil
	}
	if reference.Relation != ArtifactRelationAttached || strings.TrimSpace(reference.Filename) != reference.Filename ||
		strings.TrimSpace(reference.ContentType) != reference.ContentType ||
		strings.TrimSpace(reference.Checksum) != reference.Checksum || reference.Filename == "" ||
		reference.ContentType == "" || reference.Checksum == "" {
		return errors.New("invalid user Transcript Web artifact reference")
	}
	return nil
}

func validateServedTranscriptWebMessageArtifactReference(reference TranscriptWebMessageArtifactReference) error {
	if reference.MessageOrdinal <= 0 || reference.Ordinal < 0 || reference.SourceEventID <= 0 ||
		reference.SourceReferenceOrdinal < 0 || strings.TrimSpace(reference.ArtifactID) != reference.ArtifactID ||
		strings.TrimSpace(reference.VersionID) != reference.VersionID || reference.ArtifactID == "" ||
		reference.VersionID == "" || reference.SizeBytes < 0 ||
		(reference.Relation != ArtifactRelationProduced && reference.Relation != ArtifactRelationConsumed &&
			reference.Relation != ArtifactRelationCited && reference.Relation != ArtifactRelationAttached) ||
		(reference.Availability != ArtifactAvailable && reference.Availability != ArtifactDeleted &&
			reference.Availability != ArtifactMissing) {
		return errors.New("invalid served Transcript Web artifact reference")
	}
	if reference.RunnerAttempt != nil {
		if *reference.RunnerAttempt <= 0 {
			return errors.New("invalid served runner Transcript Web artifact reference")
		}
		if reference.Availability == ArtifactAvailable &&
			(reference.Filename == "" || reference.ContentType == "" || reference.Checksum == "") {
			return errors.New("available runner Transcript Web artifact metadata is incomplete")
		}
		return nil
	}
	if reference.Relation != ArtifactRelationAttached || reference.Filename == "" ||
		reference.ContentType == "" || reference.Checksum == "" {
		return errors.New("invalid served user Transcript Web artifact reference")
	}
	return nil
}

func validateTranscriptWebArtifactReferenceSource(
	ctx context.Context,
	tx *ImmediateTransaction,
	streamUID, branchID string,
	message TranscriptWebMessageRecord,
	reference TranscriptWebMessageArtifactReference,
) error {
	var publicationSequence int64
	var runnerAttempt sql.NullInt64
	var eventType string
	var payloadJSON []byte
	err := tx.QueryRowContext(ctx, `
		SELECT event.publication_seq,event.runner_attempt,event.event_type,event.payload_json
		FROM transcript_events event
		JOIN transcript_branch_events membership
			ON membership.stream_uid=event.stream_uid AND membership.event_id=event.event_id
		WHERE event.stream_uid=? AND membership.branch_id=? AND event.event_id=?`,
		streamUID, branchID, reference.SourceEventID,
	).Scan(&publicationSequence, &runnerAttempt, &eventType, &payloadJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTranscriptWebProjectionStale
	}
	if err != nil {
		return fmt.Errorf("read Transcript Web artifact source event: %w", err)
	}
	if publicationSequence < message.FirstPublicationSequence || publicationSequence > message.LastPublicationSequence {
		return errors.New("Transcript Web artifact source is outside its message")
	}
	if reference.RunnerAttempt != nil {
		if !runnerAttempt.Valid || runnerAttempt.Int64 != *reference.RunnerAttempt {
			return errors.New("Transcript Web runner artifact source attempt mismatch")
		}
		return nil
	}
	if runnerAttempt.Valid || (eventType != "user_message" && eventType != "user_input_response") {
		return errors.New("Transcript Web user artifact source is invalid")
	}
	var payload map[string]json.RawMessage
	if len(payloadJSON) == 0 || json.Unmarshal(payloadJSON, &payload) != nil {
		return errors.New("Transcript Web user artifact payload is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload["artifactRefs"]))
	decoder.DisallowUnknownFields()
	var persisted []UserArtifactReference
	if decoder.Decode(&persisted) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		reference.SourceReferenceOrdinal >= len(persisted) {
		return errors.New("Transcript Web user artifact payload is invalid")
	}
	value := persisted[reference.SourceReferenceOrdinal]
	if value.ArtifactID != reference.ArtifactID || value.VersionID != reference.VersionID ||
		value.Filename != reference.Filename || value.ContentType != reference.ContentType ||
		value.SizeBytes != reference.SizeBytes || value.Checksum != reference.Checksum {
		return errors.New("Transcript Web user artifact snapshot mismatch")
	}
	return nil
}

func TranscriptWebSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validTranscriptWebSHA256(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == sha256.Size
}

func matchesTranscriptWebSHA256(value []byte, expected string) bool {
	return validTranscriptWebSHA256(expected) && TranscriptWebSHA256(value) == strings.TrimSpace(expected)
}

func transcriptWebBoolToInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}
