package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type OutboxStats struct {
	Pending         int            `json:"pending"`
	Inflight        int            `json:"inflight"`
	Delivered       int            `json:"delivered"`
	DeadLetter      int            `json:"deadLetter"`
	Ready           int            `json:"ready"`
	ExpiredLeases   int            `json:"expiredLeases"`
	OldestPendingAt *time.Time     `json:"oldestPendingAt,omitempty"`
	ByTopic         map[string]int `json:"byTopic"`
}

const retiredScientificComputeSubmissionTopic = "scientific.compute.submission"

var ErrRetiredScientificComputeDeadLetter = errors.New("retired scientific compute event cannot be requeued")

func (s *Store) OutboxStats(ctx context.Context) (OutboxStats, error) {
	if s == nil || s.db == nil {
		return OutboxStats{}, errors.New("workspace store is closed")
	}
	var stats OutboxStats
	var oldest sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='inflight' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='delivered' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='dead_letter' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='pending' AND available_at_ms <= `+sqliteNowMillis+` THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='inflight' AND lease_expires_at_ms <= `+sqliteNowMillis+` THEN 1 ELSE 0 END),0),
		MIN(CASE WHEN status IN ('pending','inflight') THEN occurred_at_ms END)
		FROM workspace_outbox`).Scan(
		&stats.Pending, &stats.Inflight, &stats.Delivered, &stats.DeadLetter,
		&stats.Ready, &stats.ExpiredLeases, &oldest,
	)
	if err != nil {
		return OutboxStats{}, fmt.Errorf("read outbox stats: %w", err)
	}
	if oldest.Valid {
		value := time.UnixMilli(oldest.Int64).UTC()
		stats.OldestPendingAt = &value
	}
	stats.ByTopic = map[string]int{}
	rows, err := s.db.QueryContext(ctx, `SELECT topic, COUNT(*) FROM workspace_outbox
		WHERE status IN ('pending','inflight','dead_letter') GROUP BY topic ORDER BY topic LIMIT 256`)
	if err != nil {
		return OutboxStats{}, fmt.Errorf("read outbox topic stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var topic string
		var count int
		if err := rows.Scan(&topic, &count); err != nil {
			return OutboxStats{}, err
		}
		stats.ByTopic[topic] = count
	}
	return stats, rows.Err()
}

func (s *Store) ListOutboxDeadLetters(ctx context.Context, limit int) ([]OutboxEvent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 256 {
		limit = 256
	}
	rows, err := s.db.QueryContext(ctx, outboxSelect+` WHERE status='dead_letter' ORDER BY dead_lettered_at_ms DESC, sequence DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list outbox dead letters: %w", err)
	}
	defer rows.Close()
	events := make([]OutboxEvent, 0, limit)
	for rows.Next() {
		event, err := scanOutboxEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) RequeueOutboxDeadLetter(ctx context.Context, eventID string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	eventID = strings.TrimSpace(eventID)
	if err := validateOutboxText("event id", eventID, outboxMaxEventIDBytes, true); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE workspace_outbox SET
		status='pending', attempt_count=0, available_at_ms=`+sqliteNowMillis+`, last_error=NULL,
		claim_owner=NULL, claim_token=NULL, claimed_at_ms=NULL, lease_expires_at_ms=NULL,
		dead_lettered_at_ms=NULL, delivered_at_ms=NULL
		WHERE event_id=? AND status='dead_letter' AND topic<>?`, eventID, retiredScientificComputeSubmissionTopic)
	if err != nil {
		return fmt.Errorf("requeue outbox dead letter: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		var topic, status string
		if queryErr := s.db.QueryRowContext(ctx, `SELECT topic,status FROM workspace_outbox WHERE event_id=?`, eventID).Scan(&topic, &status); queryErr == nil &&
			topic == retiredScientificComputeSubmissionTopic && status == OutboxStatusDeadLetter {
			return ErrRetiredScientificComputeDeadLetter
		}
		return fmt.Errorf("outbox dead letter %q was not found", eventID)
	}
	s.signalOutboxWake()
	return nil
}
