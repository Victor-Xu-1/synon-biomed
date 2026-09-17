package transcript

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
)

const (
	maxDeliveryIdentityBytes = 512
	maxDeliveryLease         = 24 * time.Hour
	maxDeliveryRetryDelay    = 24 * time.Hour
	maxDeliveryAttempts      = 100
)

var deliveryErrorCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

func (r *Repository) ListDeliveryOwners(ctx context.Context, destination string, limit int) ([]string, error) {
	if r == nil || r.db == nil {
		return nil, ErrSchemaUnavailable
	}
	destination = strings.TrimSpace(destination)
	if destination == "" || len(destination) > maxDeliveryIdentityBytes || limit <= 0 || limit > 1000 {
		return nil, errors.New("bounded destination and owner limit are required")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT stream.owner_id
		FROM transcript_delivery_intents intent
		JOIN transcript_streams stream ON stream.stream_uid=intent.stream_uid
		JOIN transcript_delivery_routes route ON route.stream_uid=intent.stream_uid
			AND route.destination=intent.destination
		WHERE intent.destination=? AND intent.status IN ('pending','inflight','failed')
			AND route.status='active' AND route.current_generation=intent.route_generation
		ORDER BY stream.owner_id LIMIT ?`, destination, limit)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()
	owners := make([]string, 0, limit)
	for rows.Next() {
		var ownerID string
		if err := rows.Scan(&ownerID); err != nil {
			return nil, schemaError(err)
		}
		if ownerID = strings.TrimSpace(ownerID); ownerID != "" {
			owners = append(owners, ownerID)
		}
	}
	return owners, schemaError(rows.Err())
}

func (r *Repository) HasUnsettledDelivery(
	ctx context.Context, ownerID, destination string,
) (bool, error) {
	state, err := r.DeliverySettlementState(ctx, ownerID, destination)
	return state.Unsettled(), err
}

type DeliverySettlement struct {
	// Runnable is durable work that ClaimNextDelivery can claim now or after
	// its persisted retry/lease time. Background coordinators may schedule it.
	Runnable bool
	// ExplicitlyRecoverable is a failed intent that a reconnect/replay action
	// may deliberately move back to pending. Background coordinators must not
	// spin on it because ClaimNextDelivery never claims failed rows.
	ExplicitlyRecoverable bool
	Poisoned              bool
}

func (state DeliverySettlement) Unsettled() bool {
	return state.Runnable || state.ExplicitlyRecoverable || state.Poisoned
}

// DeliverySettlementState distinguishes background-runnable work from a
// failed intent that requires explicit replay recovery and from terminal
// poison. All three states are unsettled, but only Runnable may wake a
// background coordinator.
func (r *Repository) DeliverySettlementState(
	ctx context.Context, ownerID, destination string,
) (DeliverySettlement, error) {
	if r == nil || r.db == nil {
		return DeliverySettlement{}, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	destination = strings.TrimSpace(destination)
	if ownerID == "" || destination == "" || len(ownerID) > maxDeliveryIdentityBytes ||
		len(destination) > maxDeliveryIdentityBytes {
		return DeliverySettlement{}, errors.New("bounded owner and destination are required")
	}
	var runnable, explicitlyRecoverable, poisoned int
	err := r.db.QueryRowContext(ctx, `WITH delivery_frontier AS MATERIALIZED (
		SELECT intent.stream_uid,intent.destination,intent.route_generation,
			MIN(intent.publication_seq) AS publication_seq
		FROM transcript_delivery_intents intent
		JOIN transcript_streams stream ON stream.stream_uid=intent.stream_uid
		JOIN transcript_delivery_routes route ON route.stream_uid=intent.stream_uid
			AND route.destination=intent.destination
		WHERE stream.owner_id=? AND intent.destination=?
			AND route.status='active' AND route.current_generation=intent.route_generation
			AND intent.status NOT IN ('delivered','revoked')
		GROUP BY intent.stream_uid,intent.destination,intent.route_generation
	)
	SELECT
		COALESCE(MAX(CASE WHEN intent.status IN ('pending','inflight') THEN 1 ELSE 0 END),0),
		COALESCE(MAX(CASE WHEN intent.status='failed'
			AND intent.attempt_count BETWEEN 0 AND ?-1 THEN 1 ELSE 0 END),0),
		COALESCE(MAX(CASE WHEN intent.status='failed'
			AND intent.attempt_count NOT BETWEEN 0 AND ?-1 THEN 1 ELSE 0 END),0)
	FROM delivery_frontier frontier
	JOIN transcript_delivery_intents intent
		ON intent.stream_uid=frontier.stream_uid
		AND intent.publication_seq=frontier.publication_seq
		AND intent.destination=frontier.destination
		AND intent.route_generation=frontier.route_generation`,
		ownerID, destination, maxDeliveryAttempts, maxDeliveryAttempts).
		Scan(&runnable, &explicitlyRecoverable, &poisoned)
	return DeliverySettlement{
		Runnable: runnable != 0, ExplicitlyRecoverable: explicitlyRecoverable != 0, Poisoned: poisoned != 0,
	}, schemaError(err)
}

func (r *Repository) ActivateDeliveryRoute(
	ctx context.Context,
	ownerID, streamUID, destination string,
) (DeliveryRoute, bool, error) {
	ownerID = strings.TrimSpace(ownerID)
	streamUID = strings.TrimSpace(streamUID)
	destination = strings.TrimSpace(destination)
	if ownerID == "" || streamUID == "" || destination == "" || len(destination) > maxDeliveryIdentityBytes {
		return DeliveryRoute{}, false, errors.New("owner, stream, and bounded destination are required")
	}
	var route DeliveryRoute
	var activated bool
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		if _, err := getStreamConn(ctx, conn, streamUID, ownerID); err != nil {
			return err
		}
		var generation int64
		var status string
		var updatedAt time.Time
		err := conn.QueryRowContext(ctx, `
			SELECT current_generation,status,updated_at FROM transcript_delivery_routes
			WHERE stream_uid=? AND destination=?`, streamUID, destination).Scan(&generation, &status, &updatedAt)
		now := r.now().UTC()
		switch {
		case errors.Is(err, sql.ErrNoRows):
			generation = 1
			_, err = conn.ExecContext(ctx, `
				INSERT INTO transcript_delivery_routes(stream_uid,destination,current_generation,status,updated_at)
				VALUES(?,?,?,'active',?)`, streamUID, destination, generation, now)
			activated = true
			updatedAt = now
		case err != nil:
			return err
		case status == "active":
			// Activation is idempotent while the current route remains active.
		case status == "revoked":
			generation++
			_, err = conn.ExecContext(ctx, `
				UPDATE transcript_delivery_routes SET current_generation=?,status='active',updated_at=?
				WHERE stream_uid=? AND destination=? AND status='revoked'`, generation, now, streamUID, destination)
			activated = true
			updatedAt = now
		default:
			return ErrEventConflict
		}
		if err != nil {
			return err
		}
		route = DeliveryRoute{StreamUID: streamUID, Destination: destination, Generation: generation, Status: "active", UpdatedAt: updatedAt}
		return nil
	})
	return route, activated, schemaError(err)
}

func (r *Repository) GetDeliveryRoute(
	ctx context.Context,
	ownerID, streamUID, destination string,
) (DeliveryRoute, bool, error) {
	ownerID = strings.TrimSpace(ownerID)
	streamUID = strings.TrimSpace(streamUID)
	destination = strings.TrimSpace(destination)
	if ownerID == "" || streamUID == "" || destination == "" {
		return DeliveryRoute{}, false, errors.New("owner, stream, and destination are required")
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return DeliveryRoute{}, false, err
	}
	var route DeliveryRoute
	err := r.db.QueryRowContext(ctx, `
		SELECT stream_uid,destination,current_generation,status,updated_at
		FROM transcript_delivery_routes WHERE stream_uid=? AND destination=?`, streamUID, destination).Scan(
		&route.StreamUID, &route.Destination, &route.Generation, &route.Status, &route.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryRoute{}, false, nil
	}
	return route, err == nil, schemaError(err)
}

func activeDeliveryGenerationConn(ctx context.Context, conn *sql.Conn, streamUID, destination string) (int64, error) {
	var generation int64
	var status string
	err := conn.QueryRowContext(ctx, `
		SELECT current_generation,status FROM transcript_delivery_routes
		WHERE stream_uid=? AND destination=?`, streamUID, destination).Scan(&generation, &status)
	if errors.Is(err, sql.ErrNoRows) || err == nil && status != "active" {
		return 0, ErrDeliveryRouteInactive
	}
	return generation, err
}

func (r *Repository) ClaimNextDelivery(ctx context.Context, input ClaimDeliveryInput) (ClaimDeliveryResult, error) {
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.Destination = strings.TrimSpace(input.Destination)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if input.OwnerID == "" || input.Destination == "" || input.WorkerID == "" ||
		len(input.OwnerID) > maxDeliveryIdentityBytes || len(input.Destination) > maxDeliveryIdentityBytes ||
		len(input.WorkerID) > maxDeliveryIdentityBytes || input.TTL <= 0 || input.TTL > maxDeliveryLease {
		return ClaimDeliveryResult{}, errors.New("valid owner, destination, worker, and bounded ttl are required")
	}
	now := r.now().UTC()
	var result ClaimDeliveryResult
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		claim, found, err := nextDeliveryClaimCandidate(ctx, conn, input, now)
		if err != nil || !found {
			return err
		}
		token, digest, err := r.newClaimToken()
		if err != nil {
			return err
		}
		claim.WorkerID = input.WorkerID
		claim.ClaimToken = token
		claim.AttemptCount++
		claim.ExpiresAt = now.Add(input.TTL)
		claim.ArtifactReferences, err = artifactReferencesForEventConn(ctx, conn, claim.Event)
		if err != nil {
			return err
		}
		claim.ResolvedPayloadJSON, err = resolveEventPayloadConn(ctx, conn, claim.Event)
		if err != nil {
			return err
		}
		updated, err := conn.ExecContext(ctx, `
			UPDATE transcript_delivery_intents SET
				status='inflight',attempt_count=?,claim_token_sha256=?,claimed_by=?,
				lease_expires_at=?,next_attempt_at=NULL,updated_at=?
			WHERE stream_uid=? AND publication_seq=? AND destination=? AND route_generation=?`,
			claim.AttemptCount, digest, claim.WorkerID, claim.ExpiresAt, now,
			claim.StreamUID, claim.PublicationSeq, claim.Destination, claim.RouteGeneration,
		)
		if err != nil {
			return err
		}
		if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
			return ErrDeliveryClaimStale
		}
		result = ClaimDeliveryResult{Claim: claim, Claimed: true}
		return nil
	})
	return result, schemaError(err)
}

func nextDeliveryClaimCandidate(
	ctx context.Context,
	conn *sql.Conn,
	input ClaimDeliveryInput,
	now time.Time,
) (DeliveryClaim, bool, error) {
	row := conn.QueryRowContext(ctx, `WITH delivery_frontier AS MATERIALIZED (
		SELECT intent.stream_uid,intent.destination,intent.route_generation,
			MIN(intent.publication_seq) AS publication_seq
		FROM transcript_delivery_intents intent
		JOIN transcript_streams stream ON stream.stream_uid=intent.stream_uid
		JOIN transcript_delivery_routes route ON route.stream_uid=intent.stream_uid
			AND route.destination=intent.destination
		WHERE stream.owner_id=? AND intent.destination=?
			AND route.status='active' AND route.current_generation=intent.route_generation
			AND intent.status NOT IN ('delivered','revoked')
		GROUP BY intent.stream_uid,intent.destination,intent.route_generation
	)
		SELECT intent.stream_uid,stream.owner_id,intent.publication_seq,intent.destination,
			intent.route_generation,intent.attempt_count,
			event.event_id,event.client_message_id,event.event_type,event.source,
			event.runner_attempt,event.payload_json,event.frame_event_id,event.created_at
		FROM delivery_frontier frontier
		JOIN transcript_delivery_intents intent
			ON intent.stream_uid=frontier.stream_uid
			AND intent.publication_seq=frontier.publication_seq
			AND intent.destination=frontier.destination
			AND intent.route_generation=frontier.route_generation
		JOIN transcript_streams stream ON stream.stream_uid=intent.stream_uid
		JOIN transcript_events event ON event.stream_uid=intent.stream_uid
			AND event.publication_seq=intent.publication_seq
		WHERE ((intent.status='pending' AND (intent.next_attempt_at IS NULL OR intent.next_attempt_at<=?))
				OR (intent.status='inflight' AND intent.lease_expires_at<=?))
		-- Web realtime is a per-conversation accelerator over durable history.
		-- Prefer the newest conversation frontier so a large historical replay
		-- cannot delay the active user path; MIN(publication_seq) above still
		-- preserves strict order inside every stream. Non-Web destinations retain
		-- oldest-first delivery semantics.
		ORDER BY
			CASE WHEN intent.destination='ws' THEN event.created_at END DESC,
			CASE WHEN intent.destination<>'ws' THEN event.created_at END ASC,
			intent.stream_uid,intent.publication_seq
		LIMIT 1`, input.OwnerID, input.Destination, now, now)
	claim, err := scanDeliveryClaim(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryClaim{}, false, nil
	}
	return claim, err == nil, err
}

func (r *Repository) HeartbeatDelivery(
	ctx context.Context,
	input HeartbeatDeliveryInput,
) (HeartbeatDeliveryResult, error) {
	if err := validateDeliveryClaim(input.Claim); err != nil {
		return HeartbeatDeliveryResult{}, err
	}
	if input.TTL <= 0 || input.TTL > maxDeliveryLease {
		return HeartbeatDeliveryResult{}, errors.New("bounded positive delivery ttl is required")
	}
	now := r.now().UTC()
	expiresAt := now.Add(input.TTL)
	var result HeartbeatDeliveryResult
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		state, err := loadDeliveryClaimState(ctx, conn, input.Claim)
		if err != nil {
			return err
		}
		if state.status != "inflight" || !state.leaseExpires.Valid || !state.leaseExpires.Time.After(now) {
			return ErrDeliveryClaimStale
		}
		updated, err := conn.ExecContext(ctx, `
			UPDATE transcript_delivery_intents SET lease_expires_at=?,updated_at=?
			WHERE stream_uid=? AND publication_seq=? AND destination=? AND route_generation=?
				AND status='inflight' AND attempt_count=? AND lease_expires_at>?`,
			expiresAt, now, input.Claim.StreamUID, input.Claim.PublicationSeq, input.Claim.Destination,
			input.Claim.RouteGeneration, input.Claim.AttemptCount, now)
		if err != nil {
			return err
		}
		if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
			return ErrDeliveryClaimStale
		}
		result = HeartbeatDeliveryResult{Renewed: true, ExpiresAt: expiresAt}
		return nil
	})
	return result, schemaError(err)
}

func (r *Repository) AcknowledgeDelivery(
	ctx context.Context,
	input AcknowledgeDeliveryInput,
) (DeliveryTransitionResult, error) {
	if err := validateDeliveryClaim(input.Claim); err != nil {
		return DeliveryTransitionResult{}, err
	}
	now := r.now().UTC()
	var result DeliveryTransitionResult
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		state, err := loadDeliveryClaimState(ctx, conn, input.Claim)
		if err != nil {
			return err
		}
		if state.status == "delivered" {
			result.Status = state.status
			return nil
		}
		if state.status != "inflight" || !state.leaseExpires.Valid || !state.leaseExpires.Time.After(now) {
			return ErrDeliveryClaimStale
		}
		updated, err := conn.ExecContext(ctx, `
			UPDATE transcript_delivery_intents SET status='delivered',delivered_at=?,updated_at=?
			WHERE stream_uid=? AND publication_seq=? AND destination=? AND route_generation=?
				AND status='inflight' AND attempt_count=?`,
			now, now, input.Claim.StreamUID, input.Claim.PublicationSeq, input.Claim.Destination,
			input.Claim.RouteGeneration, input.Claim.AttemptCount)
		if err != nil {
			return err
		}
		if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
			return ErrDeliveryClaimStale
		}
		result = DeliveryTransitionResult{Applied: true, Status: "delivered"}
		return nil
	})
	return result, schemaError(err)
}

func (r *Repository) FailDelivery(
	ctx context.Context,
	input FailDeliveryInput,
) (DeliveryTransitionResult, error) {
	if err := validateDeliveryClaim(input.Claim); err != nil {
		return DeliveryTransitionResult{}, err
	}
	input.ErrorCode = strings.TrimSpace(input.ErrorCode)
	if !deliveryErrorCodePattern.MatchString(input.ErrorCode) || input.RetryAfter < 0 ||
		input.RetryAfter > maxDeliveryRetryDelay || input.MaxAttempts <= 0 || input.MaxAttempts > maxDeliveryAttempts {
		return DeliveryTransitionResult{}, errors.New("bounded delivery error code, retry delay, and max attempts are required")
	}
	now := r.now().UTC()
	var result DeliveryTransitionResult
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		state, err := loadDeliveryClaimState(ctx, conn, input.Claim)
		if err != nil {
			return err
		}
		if state.status == "pending" || state.status == "failed" {
			if state.lastErrorCode != input.ErrorCode || state.lastRetryAfter != input.RetryAfter ||
				state.lastMaxAttempts != input.MaxAttempts {
				return ErrEventConflict
			}
			result.Status = state.status
			return nil
		}
		if state.status != "inflight" || !state.leaseExpires.Valid || !state.leaseExpires.Time.After(now) {
			return ErrDeliveryClaimStale
		}
		status := "pending"
		var nextAttemptAt any = now.Add(input.RetryAfter)
		if input.Claim.AttemptCount >= input.MaxAttempts {
			status = "failed"
			nextAttemptAt = nil
		}
		updated, err := conn.ExecContext(ctx, `
			UPDATE transcript_delivery_intents SET
				status=?,next_attempt_at=?,last_error_code=?,last_retry_after_ns=?,last_max_attempts=?,updated_at=?
			WHERE stream_uid=? AND publication_seq=? AND destination=? AND route_generation=?
				AND status='inflight' AND attempt_count=?`,
			status, nextAttemptAt, input.ErrorCode, input.RetryAfter.Nanoseconds(), input.MaxAttempts, now,
			input.Claim.StreamUID, input.Claim.PublicationSeq, input.Claim.Destination,
			input.Claim.RouteGeneration, input.Claim.AttemptCount)
		if err != nil {
			return err
		}
		if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
			return ErrDeliveryClaimStale
		}
		result = DeliveryTransitionResult{Applied: true, Status: status}
		return nil
	})
	return result, schemaError(err)
}

// RecoverFailedDeliveryIntents permits an explicit reconnect to retry bounded
// projection failures without turning the background worker into a retry loop.
// Failure metadata and the lifetime attempt count remain durable audit data.
func (r *Repository) RecoverFailedDeliveryIntents(
	ctx context.Context,
	ownerID, destination string,
	limit int,
) (int64, error) {
	if r == nil || r.db == nil {
		return 0, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	destination = strings.TrimSpace(destination)
	if ownerID == "" || destination == "" || len(ownerID) > maxDeliveryIdentityBytes ||
		len(destination) > maxDeliveryIdentityBytes || limit <= 0 || limit > 1000 {
		return 0, errors.New("bounded owner, destination, and recovery limit are required")
	}
	now := r.now().UTC()
	var affected int64
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `
			UPDATE transcript_delivery_intents
			SET status='pending',claim_token_sha256=NULL,claimed_by=NULL,
				lease_expires_at=NULL,next_attempt_at=?,delivered_at=NULL,updated_at=?
			WHERE rowid IN (
				SELECT intent.rowid
				FROM transcript_delivery_intents intent
				JOIN transcript_streams stream ON stream.stream_uid=intent.stream_uid
				JOIN transcript_delivery_routes route ON route.stream_uid=intent.stream_uid
					AND route.destination=intent.destination
				JOIN transcript_events event ON event.stream_uid=intent.stream_uid
					AND event.publication_seq=intent.publication_seq
				WHERE stream.owner_id=? AND intent.destination=? AND intent.status='failed'
					AND intent.attempt_count<? AND route.status='active'
					AND route.current_generation=intent.route_generation
				ORDER BY event.created_at,intent.stream_uid,intent.publication_seq
				LIMIT ?
			)`, now, now, ownerID, destination, maxDeliveryAttempts, limit)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, schemaError(err)
}

// RecoverRetryableWebProjectionDeliveryIntents gives an exhausted Web
// projection one automatic retry only after the active read-model fence covers
// the delivery. The attempt-count equality is a durable one-shot fence: if that
// retry fails, attempt_count advances beyond last_max_attempts and background
// recovery will not select the intent again. projection_failed is retained for
// rows written before projection_stale became a distinct error code.
func (r *Repository) RecoverRetryableWebProjectionDeliveryIntents(
	ctx context.Context,
	limit int,
) (int64, error) {
	if r == nil || r.db == nil {
		return 0, ErrSchemaUnavailable
	}
	if limit <= 0 || limit > 1000 {
		return 0, errors.New("bounded Web projection recovery limit is required")
	}
	now := r.now().UTC()
	var affected int64
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `
			UPDATE transcript_delivery_intents
			SET status='pending',claim_token_sha256=NULL,claimed_by=NULL,
				lease_expires_at=NULL,next_attempt_at=?,delivered_at=NULL,updated_at=?
			WHERE rowid IN (
				SELECT intent.rowid
				FROM transcript_delivery_intents intent
				JOIN transcript_delivery_routes route ON route.stream_uid=intent.stream_uid
					AND route.destination=intent.destination
				JOIN transcript_events event ON event.stream_uid=intent.stream_uid
					AND event.publication_seq=intent.publication_seq
				JOIN transcript_branch_state branch_state ON branch_state.stream_uid=intent.stream_uid
				JOIN transcript_branch_heads head ON head.stream_uid=intent.stream_uid
					AND head.branch_id=branch_state.active_branch_id
				JOIN transcript_web_projection_state projection ON projection.stream_uid=intent.stream_uid
					AND projection.branch_id=branch_state.active_branch_id
				LEFT JOIN transcript_web_projection_dirty dirty ON dirty.stream_uid=intent.stream_uid
					AND dirty.branch_id=branch_state.active_branch_id
				WHERE intent.destination='ws' AND intent.status='failed'
					AND intent.last_error_code IN ('projection_stale','projection_failed')
					AND intent.attempt_count=intent.last_max_attempts
					AND intent.last_max_attempts BETWEEN 1 AND ?
					AND route.status='active'
					AND route.current_generation=intent.route_generation
					AND projection.projector_version=? AND projection.status='ready'
					AND projection.branch_generation=branch_state.generation
					AND projection.through_publication_seq=head.through_publication_seq
					AND projection.through_publication_seq>=intent.publication_seq
					AND projection.source_revision=head.source_revision
					AND dirty.stream_uid IS NULL
				ORDER BY event.created_at,intent.stream_uid,intent.publication_seq
				LIMIT ?
			)`, now, now, maxDeliveryAttempts, TranscriptWebProjectorVersion, limit)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, schemaError(err)
}

// RebaseCompletedWebDeliveryIntents settles obsolete incremental Web events
// against a verified final projection after a projection-stale delivery has
// recovered. The canonical user message and final replacement/terminal events
// remain pending and are still published in order, so an attached browser
// converges without replaying thousands of text deltas and runtime checkpoints
// that the final answer already supersedes. A delivered stale marker, a pending
// recovered marker, or the failed one-shot retry keeps this restart-safe when
// recovery, retry, and rebase straddle different server versions.
func (r *Repository) RebaseCompletedWebDeliveryIntents(
	ctx context.Context,
	limit int,
) (int64, error) {
	if r == nil || r.db == nil {
		return 0, ErrSchemaUnavailable
	}
	if limit <= 0 || limit > 1000 {
		return 0, errors.New("bounded completed Web delivery rebase limit is required")
	}
	now := r.now().UTC()
	var affected int64
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `WITH recovered_stream AS MATERIALIZED (
			SELECT route.stream_uid,route.destination,route.current_generation AS route_generation,
				CASE receipt.status
					WHEN 'completed' THEN (
						SELECT MAX(replacement.publication_seq)
						FROM transcript_events replacement
						JOIN transcript_branch_events membership
							ON membership.stream_uid=replacement.stream_uid
							AND membership.event_id=replacement.event_id
						WHERE replacement.stream_uid=route.stream_uid
							AND membership.branch_id=branch_state.active_branch_id
							AND replacement.event_type='assistant_message'
							AND replacement.runner_attempt=terminal.runner_attempt
							AND replacement.publication_seq<terminal.publication_seq
					)
					ELSE terminal.publication_seq
				END AS replacement_publication_seq
			FROM transcript_delivery_routes route
			JOIN transcript_branch_state branch_state ON branch_state.stream_uid=route.stream_uid
			JOIN transcript_branch_heads head ON head.stream_uid=route.stream_uid
				AND head.branch_id=branch_state.active_branch_id
			JOIN transcript_web_projection_state projection ON projection.stream_uid=route.stream_uid
				AND projection.branch_id=branch_state.active_branch_id
			LEFT JOIN transcript_web_projection_dirty dirty ON dirty.stream_uid=route.stream_uid
				AND dirty.branch_id=branch_state.active_branch_id
			JOIN transcript_events terminal ON terminal.stream_uid=route.stream_uid
				AND terminal.publication_seq=head.through_publication_seq
				AND terminal.event_type='runner_finished'
			JOIN transcript_runner_receipts receipt ON receipt.stream_uid=terminal.stream_uid
				AND receipt.event_id=terminal.event_id
				AND receipt.status IN ('completed','failed','cancelled')
			WHERE route.destination='ws' AND route.status='active'
				AND EXISTS (
					SELECT 1 FROM transcript_delivery_intents marker
					WHERE marker.stream_uid=route.stream_uid
						AND marker.destination=route.destination
						AND marker.route_generation=route.current_generation
						AND marker.last_error_code IN ('projection_stale','projection_failed')
						AND marker.last_max_attempts BETWEEN 1 AND ?
						AND (marker.status='delivered'
							OR (marker.status='pending'
								AND marker.attempt_count=marker.last_max_attempts)
							OR (marker.status='failed'
								AND marker.attempt_count=marker.last_max_attempts+1))
				)
				AND EXISTS (
					SELECT 1 FROM transcript_delivery_intents pending
					WHERE pending.stream_uid=route.stream_uid
						AND pending.destination=route.destination
						AND pending.route_generation=route.current_generation
						AND pending.status='pending'
				)
				AND projection.projector_version=? AND projection.status='ready'
				AND projection.branch_generation=branch_state.generation
				AND projection.through_publication_seq=head.through_publication_seq
				AND projection.source_revision=head.source_revision
				AND dirty.stream_uid IS NULL
			ORDER BY terminal.created_at,route.stream_uid
			LIMIT ?
		), obsolete_intent AS (
			SELECT intent.rowid
			FROM recovered_stream recovered
			JOIN transcript_delivery_intents intent ON intent.stream_uid=recovered.stream_uid
				AND intent.destination=recovered.destination
				AND intent.route_generation=recovered.route_generation
			JOIN transcript_events event ON event.stream_uid=intent.stream_uid
				AND event.publication_seq=intent.publication_seq
			WHERE recovered.replacement_publication_seq IS NOT NULL
				AND (intent.status='pending' OR (
					intent.status='failed'
					AND intent.last_error_code IN ('projection_stale','projection_failed')
					AND intent.last_max_attempts BETWEEN 1 AND ?
					AND intent.attempt_count=intent.last_max_attempts+1
				))
				AND intent.publication_seq<recovered.replacement_publication_seq
				AND event.event_type<>'user_message'
		)
		UPDATE transcript_delivery_intents
		SET status='delivered',claim_token_sha256=NULL,claimed_by=NULL,
			lease_expires_at=NULL,next_attempt_at=NULL,delivered_at=?,updated_at=?
		WHERE rowid IN (SELECT rowid FROM obsolete_intent)`,
			maxDeliveryAttempts, TranscriptWebProjectorVersion, limit,
			maxDeliveryAttempts, now, now)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, schemaError(err)
}

func (r *Repository) RevokeDeliveryRoute(
	ctx context.Context,
	ownerID, streamUID, destination string,
	routeGeneration int64,
) (int64, error) {
	ownerID = strings.TrimSpace(ownerID)
	streamUID = strings.TrimSpace(streamUID)
	destination = strings.TrimSpace(destination)
	if ownerID == "" || streamUID == "" || destination == "" || routeGeneration <= 0 {
		return 0, errors.New("owner, stream, destination, and route generation are required")
	}
	var affected int64
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		if _, err := getStreamConn(ctx, conn, streamUID, ownerID); err != nil {
			return err
		}
		if destination == "ws" {
			return ErrDeliveryRouteImmutable
		}
		var currentGeneration int64
		var status string
		err := conn.QueryRowContext(ctx, `
			SELECT current_generation,status FROM transcript_delivery_routes
			WHERE stream_uid=? AND destination=?`, streamUID, destination).Scan(&currentGeneration, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDeliveryRouteInactive
		}
		if err != nil {
			return err
		}
		if currentGeneration != routeGeneration {
			return ErrDeliveryRouteStale
		}
		if status == "revoked" {
			return nil
		}
		now := r.now().UTC()
		if _, err := conn.ExecContext(ctx, `
			UPDATE transcript_delivery_routes SET status='revoked',updated_at=?
			WHERE stream_uid=? AND destination=? AND current_generation=? AND status='active'`,
			now, streamUID, destination, routeGeneration); err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `
			UPDATE transcript_delivery_intents SET status='revoked',updated_at=?
			WHERE stream_uid=? AND destination=? AND route_generation=?
				AND status IN ('pending','inflight','failed')`,
			now, streamUID, destination, routeGeneration)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, schemaError(err)
}

func (r *Repository) GetDeliveryIntent(
	ctx context.Context,
	ownerID, streamUID string,
	publicationSeq int64,
	destination string,
	routeGeneration int64,
) (DeliveryIntent, error) {
	ownerID = strings.TrimSpace(ownerID)
	streamUID = strings.TrimSpace(streamUID)
	destination = strings.TrimSpace(destination)
	if ownerID == "" || streamUID == "" || destination == "" || publicationSeq <= 0 || routeGeneration <= 0 {
		return DeliveryIntent{}, errors.New("complete delivery intent identity is required")
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return DeliveryIntent{}, err
	}
	intent, err := scanDeliveryIntent(r.db.QueryRowContext(ctx, `
		SELECT stream_uid,publication_seq,destination,route_generation,status,attempt_count,
			COALESCE(claimed_by,''),lease_expires_at,next_attempt_at,last_error_code,
			last_retry_after_ns,last_max_attempts,delivered_at,updated_at
		FROM transcript_delivery_intents
		WHERE stream_uid=? AND publication_seq=? AND destination=? AND route_generation=?`,
		streamUID, publicationSeq, destination, routeGeneration))
	return intent, schemaError(err)
}

type deliveryClaimState struct {
	status          string
	leaseExpires    sql.NullTime
	lastErrorCode   string
	lastRetryAfter  time.Duration
	lastMaxAttempts int
}

func loadDeliveryClaimState(ctx context.Context, conn *sql.Conn, claim DeliveryClaim) (deliveryClaimState, error) {
	if _, err := getStreamConn(ctx, conn, claim.StreamUID, claim.OwnerID); err != nil {
		return deliveryClaimState{}, err
	}
	var state deliveryClaimState
	var digest []byte
	var workerID string
	var attemptCount int
	var lastRetryAfterNS int64
	err := conn.QueryRowContext(ctx, `
		SELECT status,claim_token_sha256,COALESCE(claimed_by,''),attempt_count,lease_expires_at,
			last_error_code,last_retry_after_ns,last_max_attempts
		FROM transcript_delivery_intents
		WHERE stream_uid=? AND publication_seq=? AND destination=? AND route_generation=?`,
		claim.StreamUID, claim.PublicationSeq, claim.Destination, claim.RouteGeneration,
	).Scan(&state.status, &digest, &workerID, &attemptCount, &state.leaseExpires,
		&state.lastErrorCode, &lastRetryAfterNS, &state.lastMaxAttempts)
	state.lastRetryAfter = time.Duration(lastRetryAfterNS)
	if errors.Is(err, sql.ErrNoRows) || err == nil &&
		(workerID != claim.WorkerID || attemptCount != claim.AttemptCount || !equalDigest(digest, claim.ClaimToken)) {
		return deliveryClaimState{}, ErrDeliveryClaimStale
	}
	return state, err
}

func validateDeliveryClaim(claim DeliveryClaim) error {
	if strings.TrimSpace(claim.StreamUID) == "" || strings.TrimSpace(claim.OwnerID) == "" ||
		strings.TrimSpace(claim.Destination) == "" || strings.TrimSpace(claim.WorkerID) == "" ||
		strings.TrimSpace(claim.ClaimToken) == "" || claim.PublicationSeq <= 0 ||
		claim.RouteGeneration <= 0 || claim.AttemptCount <= 0 {
		return errors.New("complete delivery claim is required")
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDeliveryClaim(row rowScanner) (DeliveryClaim, error) {
	var claim DeliveryClaim
	var source string
	var runnerAttempt sql.NullInt64
	var frameEventID sql.NullString
	var payload []byte
	err := row.Scan(
		&claim.StreamUID, &claim.OwnerID, &claim.PublicationSeq, &claim.Destination,
		&claim.RouteGeneration, &claim.AttemptCount,
		&claim.Event.EventID, &claim.Event.ClientMessageID, &claim.Event.Type, &source,
		&runnerAttempt, &payload, &frameEventID, &claim.Event.CreatedAt,
	)
	if err != nil {
		return DeliveryClaim{}, err
	}
	claim.Event.StreamUID = claim.StreamUID
	claim.Event.PublicationSeq = claim.PublicationSeq
	claim.Event.Source = EventSource(source)
	claim.Event.PayloadJSON = append([]byte(nil), payload...)
	if runnerAttempt.Valid {
		value := runnerAttempt.Int64
		claim.Event.RunnerAttempt = &value
	}
	if frameEventID.Valid {
		value := frameEventID.String
		claim.Event.FrameEventID = &value
	}
	return claim, nil
}

func scanDeliveryIntent(row rowScanner) (DeliveryIntent, error) {
	var intent DeliveryIntent
	var leaseExpires, nextAttempt, deliveredAt sql.NullTime
	var lastRetryAfterNS int64
	err := row.Scan(
		&intent.StreamUID, &intent.PublicationSeq, &intent.Destination, &intent.RouteGeneration,
		&intent.Status, &intent.AttemptCount, &intent.WorkerID, &leaseExpires, &nextAttempt,
		&intent.LastErrorCode, &lastRetryAfterNS, &intent.LastMaxAttempts, &deliveredAt, &intent.UpdatedAt,
	)
	if err != nil {
		return DeliveryIntent{}, err
	}
	intent.LeaseExpiresAt = nullableTimePointer(leaseExpires)
	intent.NextAttemptAt = nullableTimePointer(nextAttempt)
	intent.LastRetryAfter = time.Duration(lastRetryAfterNS)
	intent.DeliveredAt = nullableTimePointer(deliveredAt)
	return intent, nil
}

func nullableTimePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}
