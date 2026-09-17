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
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

const (
	OutboxStatusPending    = "pending"
	OutboxStatusInflight   = "inflight"
	OutboxStatusDelivered  = "delivered"
	OutboxStatusDeadLetter = "dead_letter"

	outboxDefaultMaxAttempts  = 8
	outboxMaxBatchSize        = 256
	outboxMaxEventIDBytes     = 256
	outboxMaxIdempotencyBytes = 512
	outboxMaxTopicBytes       = 128
	outboxMaxPartitionBytes   = 512
	outboxMaxTypeBytes        = 128
	outboxMaxAggregateBytes   = 512
	outboxMaxPayloadBytes     = 1 << 20
	outboxMaxHeaderCount      = 128
	outboxMaxHeaderKeyBytes   = 256
	outboxMaxHeaderValueBytes = 8 << 10
	outboxMaxHeadersBytes     = 64 << 10
	outboxMaxLease            = 24 * time.Hour
	outboxMaxBackoff          = 24 * time.Hour
	outboxMaxAvailableDelay   = 365 * 24 * time.Hour
)

var ErrOutboxClaimLost = errors.New("outbox claim is no longer owned by this worker")

// OutboxEvent is the durable transport envelope shared by workspace mutations
// and asynchronous delivery workers. Payload is retained as JSON so consumers
// can decode it into a topic-specific typed contract.
type OutboxEvent struct {
	Sequence       int64             `json:"sequence"`
	ID             string            `json:"id"`
	IdempotencyKey string            `json:"idempotencyKey"`
	Topic          string            `json:"topic"`
	PartitionKey   string            `json:"partitionKey"`
	Type           string            `json:"type"`
	AggregateType  string            `json:"aggregateType,omitempty"`
	AggregateID    string            `json:"aggregateId,omitempty"`
	Payload        json.RawMessage   `json:"payload"`
	Headers        map[string]string `json:"headers,omitempty"`
	Status         string            `json:"status"`
	// AttemptCount increments atomically at claim time, including a claim whose
	// worker crashes before ack or retry.
	AttemptCount   int        `json:"attemptCount"`
	MaxAttempts    int        `json:"maxAttempts"`
	ClaimOwner     string     `json:"claimOwner,omitempty"`
	ClaimToken     string     `json:"claimToken,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	OccurredAt     time.Time  `json:"occurredAt"`
	AvailableAt    time.Time  `json:"availableAt"`
	LeaseExpiresAt *time.Time `json:"leaseExpiresAt,omitempty"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
	DeadLetteredAt *time.Time `json:"deadLetteredAt,omitempty"`
}

type EnqueueOutboxInput struct {
	ID             string
	IdempotencyKey string
	Topic          string
	PartitionKey   string
	Type           string
	AggregateType  string
	AggregateID    string
	Payload        json.RawMessage
	Headers        map[string]string
	MaxAttempts    int
	AvailableAfter time.Duration
}

type ClaimOutboxInput struct {
	WorkerID string
	Topics   []string
	Limit    int
	Lease    time.Duration
}

// OutboxWake returns the current broadcast generation. Callers take this
// channel before querying durable due state; a committed mutation closes it
// and replaces the generation, so query-to-wait races cannot lose a wake.
func (s *Store) OutboxWake() <-chan struct{} {
	if s == nil {
		return nil
	}
	s.outboxWakeMu.Lock()
	defer s.outboxWakeMu.Unlock()
	if s.outboxWake == nil {
		s.outboxWake = make(chan struct{})
	}
	return s.outboxWake
}

func (s *Store) signalOutboxWake() {
	if s == nil {
		return
	}
	s.outboxWakeMu.Lock()
	if s.outboxWake == nil {
		s.outboxWake = make(chan struct{})
	}
	close(s.outboxWake)
	s.outboxWake = make(chan struct{})
	s.outboxWakeMu.Unlock()
}

// DeriveOutboxEventID returns the stable event ID for one topic-specific
// idempotency key. Reusing the same key with different content is rejected.
func DeriveOutboxEventID(topic, idempotencyKey string) string {
	digest := sha256.Sum256([]byte("synon-outbox-v1\x00" + strings.TrimSpace(topic) + "\x00" + strings.TrimSpace(idempotencyKey)))
	return "evt_" + hex.EncodeToString(digest[:])
}

// WithTransaction exposes the exact sql.Tx used by workspace mutations so the
// mutation and EnqueueOutboxTx can commit or roll back as one SQLite unit.
func (s *Store) WithTransaction(ctx context.Context, fn func(*sql.Tx) error) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	if fn == nil {
		return errors.New("workspace transaction callback is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workspace transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace transaction: %w", err)
	}
	return nil
}

func (s *Store) EnqueueOutbox(ctx context.Context, input EnqueueOutboxInput) (OutboxEvent, error) {
	var event OutboxEvent
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		event, err = s.EnqueueOutboxTx(ctx, tx, input)
		return err
	})
	return event, err
}

func (s *Store) EnqueueOutboxTx(ctx context.Context, tx *sql.Tx, input EnqueueOutboxInput) (OutboxEvent, error) {
	if tx == nil {
		return OutboxEvent{}, errors.New("outbox transaction is required")
	}
	return s.enqueueOutboxTransaction(ctx, tx, input)
}

func (s *Store) enqueueOutboxTransaction(
	ctx context.Context,
	tx workspaceTransaction,
	input EnqueueOutboxInput,
) (OutboxEvent, error) {
	if s == nil || s.db == nil {
		return OutboxEvent{}, errors.New("workspace store is closed")
	}
	if tx == nil {
		return OutboxEvent{}, errors.New("outbox transaction is required")
	}
	input.Topic = strings.TrimSpace(input.Topic)
	input.Type = strings.TrimSpace(input.Type)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.ID = strings.TrimSpace(input.ID)
	input.PartitionKey = strings.TrimSpace(input.PartitionKey)
	if err := validateOutboxText("topic", input.Topic, outboxMaxTopicBytes, true); err != nil {
		return OutboxEvent{}, err
	}
	if err := validateOutboxText("type", input.Type, outboxMaxTypeBytes, true); err != nil {
		return OutboxEvent{}, err
	}
	if input.IdempotencyKey == "" && input.ID == "" {
		return OutboxEvent{}, errors.New("outbox idempotency key or explicit event id is required")
	}
	if input.ID == "" {
		input.ID = DeriveOutboxEventID(input.Topic, input.IdempotencyKey)
	}
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = input.ID
	}
	if input.PartitionKey == "" {
		input.PartitionKey = input.Topic
	}
	if err := validateOutboxText("event id", input.ID, outboxMaxEventIDBytes, true); err != nil {
		return OutboxEvent{}, err
	}
	if err := validateOutboxText("idempotency key", input.IdempotencyKey, outboxMaxIdempotencyBytes, true); err != nil {
		return OutboxEvent{}, err
	}
	if err := validateOutboxText("partition key", input.PartitionKey, outboxMaxPartitionBytes, true); err != nil {
		return OutboxEvent{}, err
	}
	if err := validateOutboxText("aggregate type", strings.TrimSpace(input.AggregateType), outboxMaxAggregateBytes, false); err != nil {
		return OutboxEvent{}, err
	}
	if err := validateOutboxText("aggregate id", strings.TrimSpace(input.AggregateID), outboxMaxAggregateBytes, false); err != nil {
		return OutboxEvent{}, err
	}
	if input.MaxAttempts == 0 {
		input.MaxAttempts = outboxDefaultMaxAttempts
	}
	if input.MaxAttempts < 1 || input.MaxAttempts > 1000 {
		return OutboxEvent{}, errors.New("outbox max attempts must be between 1 and 1000")
	}
	if input.AvailableAfter < 0 || input.AvailableAfter > outboxMaxAvailableDelay {
		return OutboxEvent{}, fmt.Errorf("outbox available delay must be between 0 and %s", outboxMaxAvailableDelay)
	}
	payload := input.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if !json.Valid(payload) {
		return OutboxEvent{}, errors.New("outbox payload must be valid JSON")
	}
	if len(payload) > outboxMaxPayloadBytes {
		return OutboxEvent{}, fmt.Errorf("outbox payload exceeds %d bytes", outboxMaxPayloadBytes)
	}
	var canonicalPayload any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&canonicalPayload); err != nil {
		return OutboxEvent{}, fmt.Errorf("decode outbox payload: %w", err)
	}
	payload, err := json.Marshal(canonicalPayload)
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("canonicalize outbox payload: %w", err)
	}
	if len(payload) > outboxMaxPayloadBytes {
		return OutboxEvent{}, fmt.Errorf("canonical outbox payload exceeds %d bytes", outboxMaxPayloadBytes)
	}
	headers := input.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	if err := validateOutboxHeaders(headers); err != nil {
		return OutboxEvent{}, err
	}
	rawHeaders, err := json.Marshal(headers)
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("marshal outbox headers: %w", err)
	}
	if len(rawHeaders) > outboxMaxHeadersBytes {
		return OutboxEvent{}, fmt.Errorf("encoded outbox headers exceed %d bytes", outboxMaxHeadersBytes)
	}
	delayMillis := input.AvailableAfter.Milliseconds()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO workspace_outbox (
			event_id, idempotency_key, topic, partition_key, event_type,
			aggregate_type, aggregate_id, payload_json, headers_json, status,
			attempt_count, max_attempts, occurred_at_ms, available_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?,
			`+sqliteNowMillis+`, `+sqliteNowMillis+` + ?)
		ON CONFLICT(event_id) DO NOTHING`,
		input.ID, input.IdempotencyKey, input.Topic, input.PartitionKey, input.Type,
		strings.TrimSpace(input.AggregateType), strings.TrimSpace(input.AggregateID), string(payload), string(rawHeaders),
		input.MaxAttempts, delayMillis,
	)
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("insert outbox event: %w", err)
	}
	event, err := scanOutboxEvent(tx.QueryRowContext(ctx, outboxSelect+` WHERE event_id = ?`, input.ID))
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("read enqueued outbox event: %w", err)
	}
	if event.IdempotencyKey != input.IdempotencyKey || event.Topic != input.Topic || event.PartitionKey != input.PartitionKey ||
		event.Type != input.Type || event.AggregateType != strings.TrimSpace(input.AggregateType) ||
		event.AggregateID != strings.TrimSpace(input.AggregateID) || string(event.Payload) != string(payload) ||
		event.MaxAttempts != input.MaxAttempts || !equalStringMaps(event.Headers, headers) {
		return OutboxEvent{}, fmt.Errorf("outbox event id %q already identifies different content", input.ID)
	}
	// Signal at the centralized insert boundary. The write pool has one
	// connection, so a dispatcher cannot observe this generation before the
	// enclosing transaction commits or rolls back. A rollback may cause one
	// harmless empty query but can never cause delivery of uncommitted data.
	s.signalOutboxWake()
	return event, nil
}

// NextOutboxWakeAt returns the nearest durable availability or lease-expiry
// time for an open partition head. It never imposes a logical task deadline;
// it only schedules the next transport attempt after a persisted state change.
func (s *Store) NextOutboxWakeAt(ctx context.Context, topics []string) (time.Time, bool, error) {
	if s == nil || s.db == nil {
		return time.Time{}, false, errors.New("workspace store is closed")
	}
	topics = normalizeOutboxTopics(topics)
	filter := ""
	args := make([]any, 0, len(topics))
	if len(topics) > 0 {
		placeholders := make([]string, len(topics))
		for index, topic := range topics {
			if err := validateOutboxText("wake topic", topic, outboxMaxTopicBytes, true); err != nil {
				return time.Time{}, false, err
			}
			placeholders[index] = "?"
			args = append(args, topic)
		}
		filter = " AND event.topic IN (" + strings.Join(placeholders, ",") + ")"
	}
	var wakeMillis sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT MIN(CASE WHEN event.status='pending' THEN event.available_at_ms ELSE event.lease_expires_at_ms END)
		FROM workspace_outbox AS event
		WHERE event.status IN ('pending','inflight')`+filter+`
			AND NOT EXISTS (
				SELECT 1 FROM workspace_outbox AS predecessor
				WHERE predecessor.partition_key=event.partition_key
					AND predecessor.sequence<event.sequence
					AND predecessor.status IN ('pending','inflight')
			)`, args...).Scan(&wakeMillis)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("find next outbox wake time: %w", err)
	}
	if !wakeMillis.Valid {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(wakeMillis.Int64).UTC(), true, nil
}

func (s *Store) ClaimOutbox(ctx context.Context, input ClaimOutboxInput) ([]OutboxEvent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if input.WorkerID == "" {
		return nil, errors.New("outbox worker id is required")
	}
	if input.Limit <= 0 {
		input.Limit = 32
	}
	if input.Limit > outboxMaxBatchSize {
		input.Limit = outboxMaxBatchSize
	}
	if input.Lease <= 0 {
		return nil, errors.New("outbox claim lease must be positive")
	}
	if input.Lease > outboxMaxLease {
		return nil, fmt.Errorf("outbox claim lease exceeds %s", outboxMaxLease)
	}
	topics := normalizeOutboxTopics(input.Topics)
	for _, topic := range topics {
		if err := validateOutboxText("claim topic", topic, outboxMaxTopicBytes, true); err != nil {
			return nil, err
		}
	}
	if err := validateOutboxText("worker id", input.WorkerID, outboxMaxEventIDBytes, true); err != nil {
		return nil, err
	}
	// Empty dispatch is the steady state. Keep it read-only so idle dispatchers
	// neither contend for SQLite's single writer nor execute the expensive
	// partition-fair claim query when there is no eligible work. These states
	// are deliberately separate: combining them with OR made SQLite choose the
	// topic index and walk every delivered event instead of rejecting by status.
	workQuery, workArgs := outboxAvailableWorkQuery(topics)
	var hasWork int
	if err := s.db.QueryRowContext(ctx, workQuery, workArgs...).Scan(&hasWork); err != nil {
		return nil, fmt.Errorf("check outbox claim work: %w", err)
	}
	if hasWork == 0 {
		return nil, nil
	}
	claimToken := uuid.NewString()
	args := make([]any, 0, len(topics)+5)
	args = append(args, input.WorkerID, claimToken, input.Lease.Milliseconds())
	filter := ""
	if len(topics) > 0 {
		placeholders := make([]string, len(topics))
		for i, topic := range topics {
			placeholders[i] = "?"
			args = append(args, topic)
		}
		filter = " AND topic IN (" + strings.Join(placeholders, ",") + ")"
	}
	args = append(args, input.Limit)
	query := `
		WITH partition_progress AS (
			SELECT partition_key,
				SUM(CASE WHEN status IN ('delivered', 'dead_letter') THEN 1 ELSE 0 END) AS completed_count
			FROM workspace_outbox GROUP BY partition_key
		), eligible AS (
			SELECT event.sequence, progress.completed_count
			FROM workspace_outbox AS event
			JOIN partition_progress AS progress ON progress.partition_key = event.partition_key
			WHERE ((event.status = 'pending' AND event.attempt_count < event.max_attempts)
				OR (event.status = 'inflight' AND event.lease_expires_at_ms <= ` + sqliteNowMillis + `))
				AND event.available_at_ms <= ` + sqliteNowMillis + filter + `
				AND NOT EXISTS (
					SELECT 1 FROM workspace_outbox AS predecessor
					WHERE predecessor.partition_key = event.partition_key
						AND predecessor.sequence < event.sequence
						AND predecessor.status IN ('pending', 'inflight')
				)
		), candidates AS (
			SELECT sequence FROM eligible ORDER BY completed_count, sequence LIMIT ?
		)
		UPDATE workspace_outbox
		SET status = 'inflight', claim_owner = ?, claim_token = ?, attempt_count = attempt_count + 1,
			claimed_at_ms = ` + sqliteNowMillis + `,
			lease_expires_at_ms = ` + sqliteNowMillis + ` + ?
		WHERE sequence IN (SELECT sequence FROM candidates)
			AND ((status = 'pending' AND attempt_count < max_attempts)
				OR (status = 'inflight' AND lease_expires_at_ms <= ` + sqliteNowMillis + `))
		RETURNING ` + outboxColumns
	// UPDATE parameters follow the CTE parameters in textual order.
	queryArgs := append([]any{}, args[3:]...)
	queryArgs = append(queryArgs, args[0], args[1], args[2])
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("claim outbox batch: %w", err)
	}
	defer rows.Close()
	events := make([]OutboxEvent, 0, input.Limit)
	for rows.Next() {
		event, err := scanOutboxEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan claimed outbox event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed outbox batch: %w", err)
	}
	return events, nil
}

func outboxAvailableWorkQuery(topics []string) (string, []any) {
	workFilter := ""
	workArgs := make([]any, 0, len(topics)*2)
	if len(topics) > 0 {
		placeholders := make([]string, len(topics))
		for index, topic := range topics {
			placeholders[index] = "?"
			workArgs = append(workArgs, topic)
		}
		workFilter = " AND topic IN (" + strings.Join(placeholders, ",") + ")"
		for _, topic := range topics {
			workArgs = append(workArgs, topic)
		}
	}
	return `
		SELECT EXISTS(
			SELECT 1 FROM workspace_outbox INDEXED BY workspace_outbox_ready_idx
			WHERE status = 'pending' AND available_at_ms <= ` + sqliteNowMillis + `
				AND attempt_count < max_attempts` + workFilter + `
			UNION ALL
			SELECT 1 FROM workspace_outbox INDEXED BY workspace_outbox_ready_idx
			WHERE status = 'inflight' AND lease_expires_at_ms <= ` + sqliteNowMillis + workFilter + `
			LIMIT 1
		)`, workArgs
}

func (s *Store) AckOutbox(ctx context.Context, eventID, claimToken string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE workspace_outbox
		SET status = 'delivered', delivered_at_ms = `+sqliteNowMillis+`,
			claim_owner = NULL, claim_token = NULL, claimed_at_ms = NULL, lease_expires_at_ms = NULL,
			last_error = NULL
		WHERE event_id = ? AND status = 'inflight' AND claim_token = ?`,
		strings.TrimSpace(eventID), strings.TrimSpace(claimToken))
	if err != nil {
		return fmt.Errorf("ack outbox event: %w", err)
	}
	if err := requireOutboxClaim(result); err != nil {
		return err
	}
	s.signalOutboxWake()
	return nil
}

// RetryOutbox records one failed delivery. max attempts transitions directly
// to dead_letter; otherwise DB time controls the next eligible delivery.
func (s *Store) RetryOutbox(ctx context.Context, eventID, claimToken, deliveryError string, backoff time.Duration) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	if backoff < 0 || backoff > outboxMaxBackoff {
		return false, fmt.Errorf("outbox retry backoff must be between 0 and %s", outboxMaxBackoff)
	}
	deliveryError = strings.TrimSpace(deliveryError)
	if len(deliveryError) > 4096 {
		deliveryError = deliveryError[:4096]
	}
	var dead bool
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var attempts, maxAttempts int
		if err := tx.QueryRowContext(ctx, `
			SELECT attempt_count, max_attempts FROM workspace_outbox
			WHERE event_id = ? AND status = 'inflight' AND claim_token = ?`,
			strings.TrimSpace(eventID), strings.TrimSpace(claimToken)).Scan(&attempts, &maxAttempts); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrOutboxClaimLost
			}
			return fmt.Errorf("read outbox retry claim: %w", err)
		}
		dead = attempts >= maxAttempts
		status := OutboxStatusPending
		deadAt := "NULL"
		if dead {
			status = OutboxStatusDeadLetter
			deadAt = sqliteNowMillis
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE workspace_outbox
			SET status = ?, attempt_count = ?, last_error = ?,
				available_at_ms = `+sqliteNowMillis+` + ?, dead_lettered_at_ms = `+deadAt+`,
				claim_owner = NULL, claim_token = NULL, claimed_at_ms = NULL, lease_expires_at_ms = NULL
			WHERE event_id = ? AND status = 'inflight' AND claim_token = ?`,
			status, attempts, deliveryError, backoff.Milliseconds(), strings.TrimSpace(eventID), strings.TrimSpace(claimToken))
		if err != nil {
			return fmt.Errorf("retry outbox event: %w", err)
		}
		return requireOutboxClaim(result)
	})
	if err == nil {
		s.signalOutboxWake()
	}
	return dead, err
}

func (s *Store) GetOutboxEvent(ctx context.Context, eventID string) (OutboxEvent, error) {
	if s == nil || s.db == nil {
		return OutboxEvent{}, errors.New("workspace store is closed")
	}
	event, err := scanOutboxEvent(s.db.QueryRowContext(ctx, outboxSelect+` WHERE event_id = ?`, strings.TrimSpace(eventID)))
	if errors.Is(err, sql.ErrNoRows) {
		return OutboxEvent{}, fmt.Errorf("outbox event %q not found", strings.TrimSpace(eventID))
	}
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("get outbox event: %w", err)
	}
	return event, nil
}

func (s *Store) CountOutboxEvents(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_outbox`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count outbox events: %w", err)
	}
	return count, nil
}

const sqliteNowMillis = `CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)`

const outboxColumns = `sequence, event_id, idempotency_key, topic, partition_key, event_type,
	aggregate_type, aggregate_id, payload_json, headers_json, status, attempt_count, max_attempts,
	claim_owner, claim_token, last_error, occurred_at_ms, available_at_ms, lease_expires_at_ms,
	delivered_at_ms, dead_lettered_at_ms`

const outboxSelect = `SELECT ` + outboxColumns + ` FROM workspace_outbox`

type outboxRowScanner interface {
	Scan(...any) error
}

func scanOutboxEvent(row outboxRowScanner) (OutboxEvent, error) {
	var event OutboxEvent
	var payload, headers string
	var claimOwner, claimToken, lastError sql.NullString
	var occurredAt, availableAt int64
	var leaseExpiresAt, deliveredAt, deadLetteredAt sql.NullInt64
	if err := row.Scan(
		&event.Sequence, &event.ID, &event.IdempotencyKey, &event.Topic, &event.PartitionKey, &event.Type,
		&event.AggregateType, &event.AggregateID, &payload, &headers, &event.Status, &event.AttemptCount, &event.MaxAttempts,
		&claimOwner, &claimToken, &lastError, &occurredAt, &availableAt, &leaseExpiresAt, &deliveredAt, &deadLetteredAt,
	); err != nil {
		return OutboxEvent{}, err
	}
	event.Payload = json.RawMessage(payload)
	if err := json.Unmarshal([]byte(headers), &event.Headers); err != nil {
		return OutboxEvent{}, fmt.Errorf("decode outbox headers: %w", err)
	}
	event.ClaimOwner = claimOwner.String
	event.ClaimToken = claimToken.String
	event.LastError = lastError.String
	event.OccurredAt = time.UnixMilli(occurredAt).UTC()
	event.AvailableAt = time.UnixMilli(availableAt).UTC()
	event.LeaseExpiresAt = nullableMillisTime(leaseExpiresAt)
	event.DeliveredAt = nullableMillisTime(deliveredAt)
	event.DeadLetteredAt = nullableMillisTime(deadLetteredAt)
	return event, nil
}

func nullableMillisTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	t := time.UnixMilli(value.Int64).UTC()
	return &t
}

func requireOutboxClaim(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read outbox affected rows: %w", err)
	}
	if rows != 1 {
		return ErrOutboxClaimLost
	}
	return nil
}

func normalizeOutboxTopics(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func equalStringMaps(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func validateOutboxText(label, value string, maximum int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("outbox %s is required", label)
	}
	if len(value) > maximum {
		return fmt.Errorf("outbox %s exceeds %d bytes", label, maximum)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("outbox %s contains a control character", label)
		}
	}
	return nil
}

func validateOutboxHeaders(headers map[string]string) error {
	if len(headers) > outboxMaxHeaderCount {
		return fmt.Errorf("outbox headers exceed %d entries", outboxMaxHeaderCount)
	}
	total := 0
	for key, value := range headers {
		if key != strings.TrimSpace(key) || !validOutboxHeaderName(key) {
			return fmt.Errorf("outbox header name %q is not a valid HTTP field name", key)
		}
		if err := validateOutboxText("header name", key, outboxMaxHeaderKeyBytes, true); err != nil {
			return err
		}
		if err := validateOutboxText("header value", value, outboxMaxHeaderValueBytes, false); err != nil {
			return err
		}
		total += len(key) + len(value)
		if total > outboxMaxHeadersBytes {
			return fmt.Errorf("outbox headers exceed %d bytes", outboxMaxHeadersBytes)
		}
	}
	return nil
}

func validOutboxHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		c := value[index]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}
