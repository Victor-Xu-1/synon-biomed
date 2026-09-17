package transcript

import (
	"context"
	"errors"
	"strings"
)

const maxVisibleWebMessageSearchCandidates = 2000

const searchVisibleWebMessagesSQL = `
	WITH owner_streams AS MATERIALIZED (
		SELECT stream_uid,root_frame_id
		FROM transcript_streams
		WHERE owner_id=? AND kind='frame_ref' AND root_frame_id<>''
	)
	SELECT stream.root_frame_id,message.message_id,message.message_json,message.updated_at
	FROM owner_streams AS stream
	JOIN transcript_branch_state AS branch_state
		ON branch_state.stream_uid=stream.stream_uid
	JOIN transcript_web_projection_state AS projection
		ON projection.stream_uid=stream.stream_uid
			AND projection.branch_id=branch_state.active_branch_id
			AND projection.branch_generation=branch_state.generation
	JOIN transcript_web_messages AS message
		ON message.stream_uid=projection.stream_uid
			AND message.branch_id=projection.branch_id
	WHERE projection.status='ready'
		AND message.visible=1
		AND lower(message.message_json) LIKE lower(?) ESCAPE '\'
	ORDER BY message.updated_at DESC,message.ordinal DESC
	LIMIT ?
`

// SearchVisibleWebMessagesInput bounds one owner-scoped search over the
// transcript-authoritative web projection.
type SearchVisibleWebMessagesInput struct {
	OwnerID        string
	Query          string
	CandidateLimit int
}

// VisibleWebMessageSearchHit is deliberately projection-shaped. The server
// owns presentation parsing and ranking while the repository owns branch,
// visibility, readiness, and tenant isolation.
type VisibleWebMessageSearchHit struct {
	RootFrameID string
	MessageID   string
	MessageJSON string
	UpdatedAt   string
}

// SearchVisibleWebMessages searches only visible messages from the active,
// ready transcript projection. It never consults legacy message mirrors.
func (r *Repository) SearchVisibleWebMessages(
	ctx context.Context,
	input SearchVisibleWebMessagesInput,
) ([]VisibleWebMessageSearchHit, error) {
	db := r.readDatabase()
	if db == nil {
		return nil, errors.New("transcript repository is closed")
	}
	ownerID := strings.TrimSpace(input.OwnerID)
	if ownerID == "" {
		return nil, errors.New("transcript search owner id is required")
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return []VisibleWebMessageSearchHit{}, nil
	}
	limit := input.CandidateLimit
	if limit <= 0 {
		limit = 200
	}
	if limit > maxVisibleWebMessageSearchCandidates {
		limit = maxVisibleWebMessageSearchCandidates
	}
	pattern := "%" + escapeTranscriptLikePattern(query) + "%"
	rows, err := db.QueryContext(ctx, searchVisibleWebMessagesSQL, ownerID, pattern, limit)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()

	hits := make([]VisibleWebMessageSearchHit, 0, limit)
	for rows.Next() {
		var hit VisibleWebMessageSearchHit
		if err := rows.Scan(&hit.RootFrameID, &hit.MessageID, &hit.MessageJSON, &hit.UpdatedAt); err != nil {
			return nil, schemaError(err)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, schemaError(err)
	}
	return hits, nil
}

func escapeTranscriptLikePattern(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}
