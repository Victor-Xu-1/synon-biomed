package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	maxCompactionSummaryBytes  = 1 << 20
	maxCompactionMessagesBytes = 64 << 20
	maxCompactionCreateRetries = 4
)

type CompactionArchive struct {
	ID              string           `json:"id"`
	FrameID         string           `json:"frame_id"`
	CompactionIndex int              `json:"compaction_index"`
	MessageCount    int              `json:"message_count"`
	TokenCount      *int             `json:"token_count"`
	Summary         string           `json:"summary"`
	Messages        []map[string]any `json:"messages,omitempty"`
	SourceEventID   *int64           `json:"-"`
	CreatedAt       time.Time        `json:"created_at"`
}

type CreateCompactionArchiveInput struct {
	ID              string
	FrameID         string
	CompactionIndex *int
	MessageCount    int
	TokenCount      *int
	Summary         string
	Messages        []map[string]any
	SourceEventID   *int64
	CreatedAt       time.Time
}

func (s *Store) CreateCompactionArchive(input CreateCompactionArchiveInput) (CompactionArchive, error) {
	if s == nil || s.db == nil {
		return CompactionArchive{}, errors.New("workspace store is closed")
	}
	input.ID = strings.TrimSpace(input.ID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.Summary = strings.TrimSpace(input.Summary)
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	if input.FrameID == "" {
		return CompactionArchive{}, errors.New("compaction frame id is required")
	}
	if input.CompactionIndex != nil && *input.CompactionIndex < 0 {
		return CompactionArchive{}, errors.New("compaction index must be non-negative")
	}
	if input.MessageCount < 0 {
		return CompactionArchive{}, errors.New("compaction message count must be non-negative")
	}
	if input.TokenCount != nil && *input.TokenCount < 0 {
		return CompactionArchive{}, errors.New("compaction token count must be non-negative")
	}
	if len(input.Summary) > maxCompactionSummaryBytes {
		return CompactionArchive{}, fmt.Errorf("compaction summary exceeds %d bytes", maxCompactionSummaryBytes)
	}
	if input.Messages == nil {
		input.Messages = []map[string]any{}
	}
	rawMessages, err := json.Marshal(input.Messages)
	if err != nil {
		return CompactionArchive{}, fmt.Errorf("encode compaction messages: %w", err)
	}
	if len(rawMessages) > maxCompactionMessagesBytes {
		return CompactionArchive{}, fmt.Errorf("compaction messages exceed %d bytes", maxCompactionMessagesBytes)
	}
	if input.MessageCount != len(input.Messages) {
		return CompactionArchive{}, fmt.Errorf("compaction message count %d does not match %d messages", input.MessageCount, len(input.Messages))
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = s.now().UTC()
	} else {
		input.CreatedAt = input.CreatedAt.UTC()
	}
	if input.SourceEventID != nil {
		if *input.SourceEventID <= 0 {
			return CompactionArchive{}, errors.New("compaction source event id must be positive")
		}
		if existing, found, err := s.compactionArchiveBySourceEvent(input.FrameID, *input.SourceEventID); err != nil {
			return CompactionArchive{}, err
		} else if found {
			if err := verifyCompactionIdempotency(existing, input, rawMessages); err != nil {
				return CompactionArchive{}, err
			}
			return existing, nil
		}
	}
	for attempt := 0; attempt < maxCompactionCreateRetries; attempt++ {
		archive, err := s.createCompactionArchiveAttempt(input, rawMessages)
		if err == nil {
			return archive, nil
		}
		if input.CompactionIndex != nil && isCompactionIndexConflict(err) {
			existing, found, lookupErr := s.GetCompactionArchive(input.FrameID, *input.CompactionIndex)
			if lookupErr != nil {
				return CompactionArchive{}, lookupErr
			}
			if found {
				if verifyErr := verifyCompactionIdempotency(existing, input, rawMessages); verifyErr != nil {
					return CompactionArchive{}, verifyErr
				}
				return existing, nil
			}
		}
		if !isCompactionIndexConflict(err) || input.CompactionIndex != nil {
			return CompactionArchive{}, err
		}
	}
	return CompactionArchive{}, errors.New("allocate compaction index after concurrent writes")
}

func (s *Store) createCompactionArchiveAttempt(input CreateCompactionArchiveInput, rawMessages []byte) (CompactionArchive, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CompactionArchive{}, fmt.Errorf("begin compaction archive: %w", err)
	}
	defer tx.Rollback()
	var frameExists string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM frames WHERE id = ?`, input.FrameID).Scan(&frameExists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CompactionArchive{}, fmt.Errorf("frame %q does not exist", input.FrameID)
		}
		return CompactionArchive{}, fmt.Errorf("find compaction frame: %w", err)
	}
	index := 0
	if input.CompactionIndex == nil {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(compaction_index), -1) + 1 FROM compaction_archives WHERE frame_id = ?`, input.FrameID).Scan(&index); err != nil {
			return CompactionArchive{}, fmt.Errorf("allocate compaction index: %w", err)
		}
	} else {
		index = *input.CompactionIndex
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO compaction_archives
		(id, frame_id, compaction_index, message_count, token_count, summary, messages, source_event_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.ID, input.FrameID, index, input.MessageCount, nullableInt(input.TokenCount),
		input.Summary, string(rawMessages), nullableInt64(input.SourceEventID), input.CreatedAt,
	)
	if err != nil {
		if input.SourceEventID != nil && isCompactionSourceConflict(err) {
			_ = tx.Rollback()
			existing, found, lookupErr := s.compactionArchiveBySourceEvent(input.FrameID, *input.SourceEventID)
			if lookupErr != nil {
				return CompactionArchive{}, lookupErr
			}
			if found {
				if verifyErr := verifyCompactionIdempotency(existing, input, rawMessages); verifyErr != nil {
					return CompactionArchive{}, verifyErr
				}
				return existing, nil
			}
		}
		return CompactionArchive{}, fmt.Errorf("insert compaction archive: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CompactionArchive{}, fmt.Errorf("commit compaction archive: %w", err)
	}
	archive, found, err := s.GetCompactionArchive(input.FrameID, index)
	if err != nil {
		return CompactionArchive{}, err
	}
	if !found {
		return CompactionArchive{}, errors.New("compaction archive disappeared after commit")
	}
	return archive, nil
}

func (s *Store) ListCompactionArchives(frameID string) ([]CompactionArchive, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return nil, errors.New("compaction frame id is required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, frame_id, compaction_index, message_count, token_count,
		       summary, source_event_id, created_at
		FROM compaction_archives WHERE frame_id = ?
		ORDER BY compaction_index`, frameID)
	if err != nil {
		return nil, fmt.Errorf("list compaction archives: %w", err)
	}
	defer rows.Close()
	archives := make([]CompactionArchive, 0)
	for rows.Next() {
		archive, err := scanCompactionArchiveMetadata(rows)
		if err != nil {
			return nil, err
		}
		archives = append(archives, archive)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compaction archives: %w", err)
	}
	return archives, nil
}

func (s *Store) GetCompactionArchive(frameID string, index int) (CompactionArchive, bool, error) {
	if s == nil || s.db == nil {
		return CompactionArchive{}, false, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" || index < 0 {
		return CompactionArchive{}, false, errors.New("compaction frame id and non-negative index are required")
	}
	return scanCompactionArchive(s.db.QueryRowContext(context.Background(), `
		SELECT id, frame_id, compaction_index, message_count, token_count,
		       summary, messages, source_event_id, created_at
		FROM compaction_archives WHERE frame_id = ? AND compaction_index = ?`, frameID, index))
}

func (s *Store) ListCompactionMessages(frameID string, through int) ([]map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	messages := make([]map[string]any, 0)
	if through < 0 {
		return messages, nil
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return nil, errors.New("compaction frame id is required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT messages
		FROM compaction_archives
		WHERE frame_id = ? AND compaction_index <= ?
		ORDER BY compaction_index`, frameID, through)
	if err != nil {
		return nil, fmt.Errorf("list compaction messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rawMessages string
		if err := rows.Scan(&rawMessages); err != nil {
			return nil, fmt.Errorf("scan compaction messages: %w", err)
		}
		if len(rawMessages) > maxCompactionMessagesBytes {
			continue
		}
		var archiveMessages []map[string]any
		if err := json.Unmarshal([]byte(rawMessages), &archiveMessages); err != nil {
			// v1.1 uses Promise.allSettled and ignores an individual missing or
			// unreadable archive while preserving every other archive in order.
			continue
		}
		for _, message := range archiveMessages {
			if !isTruthyCompactionBoundary(message["_compact_boundary"]) {
				messages = append(messages, message)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compaction messages: %w", err)
	}
	return messages, nil
}

func isTruthyCompactionBoundary(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case int:
		return typed != 0
	case int8:
		return typed != 0
	case int16:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint8:
		return typed != 0
	case uint16:
		return typed != 0
	case uint32:
		return typed != 0
	case uint64:
		return typed != 0
	default:
		// JSON arrays and objects are truthy in JavaScript, including empty ones.
		return true
	}
}

func (s *Store) compactionArchiveBySourceEvent(frameID string, sourceEventID int64) (CompactionArchive, bool, error) {
	return scanCompactionArchive(s.db.QueryRowContext(context.Background(), `
		SELECT id, frame_id, compaction_index, message_count, token_count,
		       summary, messages, source_event_id, created_at
		FROM compaction_archives WHERE frame_id = ? AND source_event_id = ?`, frameID, sourceEventID))
}

type compactionArchiveScanner interface {
	Scan(...any) error
}

func scanCompactionArchive(scanner compactionArchiveScanner) (CompactionArchive, bool, error) {
	var archive CompactionArchive
	var tokenCount sql.NullInt64
	var sourceEventID sql.NullInt64
	var rawMessages string
	err := scanner.Scan(
		&archive.ID, &archive.FrameID, &archive.CompactionIndex, &archive.MessageCount,
		&tokenCount, &archive.Summary, &rawMessages, &sourceEventID, &archive.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CompactionArchive{}, false, nil
	}
	if err != nil {
		return CompactionArchive{}, false, fmt.Errorf("scan compaction archive: %w", err)
	}
	if tokenCount.Valid {
		value := int(tokenCount.Int64)
		archive.TokenCount = &value
	}
	if sourceEventID.Valid {
		value := sourceEventID.Int64
		archive.SourceEventID = &value
	}
	if err := json.Unmarshal([]byte(rawMessages), &archive.Messages); err != nil {
		return CompactionArchive{}, false, fmt.Errorf("decode compaction archive %s messages: %w", archive.ID, err)
	}
	if archive.Messages == nil {
		archive.Messages = []map[string]any{}
	}
	return archive, true, nil
}

func scanCompactionArchiveMetadata(scanner compactionArchiveScanner) (CompactionArchive, error) {
	var archive CompactionArchive
	var tokenCount sql.NullInt64
	var sourceEventID sql.NullInt64
	if err := scanner.Scan(
		&archive.ID, &archive.FrameID, &archive.CompactionIndex, &archive.MessageCount,
		&tokenCount, &archive.Summary, &sourceEventID, &archive.CreatedAt,
	); err != nil {
		return CompactionArchive{}, fmt.Errorf("scan compaction archive metadata: %w", err)
	}
	if tokenCount.Valid {
		value := int(tokenCount.Int64)
		archive.TokenCount = &value
	}
	if sourceEventID.Valid {
		value := sourceEventID.Int64
		archive.SourceEventID = &value
	}
	return archive, nil
}

func verifyCompactionIdempotency(existing CompactionArchive, input CreateCompactionArchiveInput, rawMessages []byte) error {
	existingMessages, err := json.Marshal(existing.Messages)
	if err != nil {
		return fmt.Errorf("encode existing compaction messages: %w", err)
	}
	if existing.MessageCount != input.MessageCount || !sameOptionalInt(existing.TokenCount, input.TokenCount) ||
		existing.Summary != input.Summary || string(existingMessages) != string(rawMessages) {
		if input.SourceEventID != nil {
			return fmt.Errorf("compaction source event %d was reused with a different payload", *input.SourceEventID)
		}
		if input.CompactionIndex != nil {
			return fmt.Errorf("compaction index %d was reused with a different payload", *input.CompactionIndex)
		}
		return errors.New("compaction identity was reused with a different payload")
	}
	return nil
}

func sameOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func isCompactionIndexConflict(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint failed") && strings.Contains(message, "compaction_archives.frame_id") && strings.Contains(message, "compaction_archives.compaction_index")
}

func isCompactionSourceConflict(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint failed") && strings.Contains(message, "compaction_archives.source_event_id")
}

func (s *Store) activateArchivedCompactionArchives(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	return s.activateArchivedCompactionArchivesWithExecutor(ctx, s.db)
}

func (s *Store) activateArchivedCompactionArchivesWithExecutor(ctx context.Context, executor schemaMigrationExecutor) error {
	var tableName string
	err := executor.QueryRowContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name = 'legacy_v11_compaction_archives'`).Scan(&tableName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect archived compaction table: %w", err)
	}
	_, err = executor.ExecContext(ctx, `
		INSERT OR IGNORE INTO compaction_archives
		(id, frame_id, compaction_index, message_count, token_count, summary, messages, created_at)
		SELECT id, frame_id, compaction_index, message_count, token_count,
		       summary, messages,
		       CASE
		         WHEN typeof(created_at) = 'integer' AND created_at > 100000000000
		           THEN datetime(created_at / 1000.0, 'unixepoch')
		         WHEN typeof(created_at) = 'integer'
		           THEN datetime(created_at, 'unixepoch')
		         ELSE created_at
		       END
		FROM legacy_v11_compaction_archives`)
	if err != nil {
		return fmt.Errorf("activate archived compaction rows: %w", err)
	}
	var mismatched int
	if err := executor.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM legacy_v11_compaction_archives AS legacy
		JOIN frames AS frame ON frame.id = legacy.frame_id
		LEFT JOIN compaction_archives AS active ON active.id = legacy.id
		  AND active.frame_id = legacy.frame_id
		  AND active.compaction_index = legacy.compaction_index
		  AND active.message_count = legacy.message_count
		  AND active.token_count IS legacy.token_count
		  AND active.summary = legacy.summary
		  AND active.messages = legacy.messages
		WHERE active.id IS NULL`).Scan(&mismatched); err != nil {
		return fmt.Errorf("verify archived compaction activation: %w", err)
	}
	if mismatched != 0 {
		return fmt.Errorf("activate archived compaction rows: %d rows conflicted with active storage", mismatched)
	}
	return nil
}
